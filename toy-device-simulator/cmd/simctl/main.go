package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultConfig = "configs/manager.yaml"
	defaultListen = "127.0.0.1:8090"
	pidPath       = "data/manager.pid"
	exePath       = "data/manager.exe"
	logPath       = "data/manager.log"
)

const usage = `simctl — 对着本仓 manager 的任务级 CLI。一律 JSON 到 stdout，没有 --json 开关。

在 toy-device-simulator/ 下运行。人和 agent 共用同一个 manager。

用法:
  simctl [--config FILE] [--listen ADDR] <动词> [参数]

全局（任意动词前后都可写）:
  --config  manager YAML，默认 configs/manager.yaml；up 会原样传给 manager
  --listen  manager 地址，默认 127.0.0.1:8090（cmd/manager 的 --listen，不在 yaml 里）

动词:
  up        后台起 manager。先 go build -o data/manager.exe ./cmd/manager 再拉起。
            幂等，不自动关。pid → data/manager.pid，日志追加 data/manager.log。
            探活 GET /devices。Windows 上 simctl 退出后进程继续活着。
  down      按 pid 文件停 manager
  status    manager 是否活着，几台设备
  devices   列设备。过滤：--env 环境名；--enterprise / --device-type 简称（不是名称）
  assets    列音频库
  run       选设备 → 送话 → 读判语（核心）
            位置参数 = device_id（可省，靠过滤选 N 台；选中 0 台报错退出）
            --asset ID           必填，音频库资产
            --env NAME           环境名
            --enterprise SHORT   厂商简称
            --device-type SHORT  设备类型简称
            --parallel           N 台并行（默认串行）；输出始终是数组
            --dirty              Running 且 overridden 时放行，否则报错不动它
            Created/Stopped 且 overridden 时自动 POST /config/reset 再 start（无声）
            没启动就 start + wait_ready
  turn      一轮的事件流与帧统计
            位置参数 device_id；--turn ID --instance ID 必填
  audio     把上行或下行音频落到文件（stdout 仍是 JSON 指针）
            位置参数 device_id
            --turn ID --instance ID --side uplink|downlink --out FILE
  history   列这台设备的每一次运行（含跨重启）
            位置参数 device_id
`

var (
	stdout io.Writer    = os.Stdout
	stderr io.Writer    = os.Stderr
	httpc  *http.Client // nil = http.DefaultClient
)

var errDirty = errors.New("这台在跑且当前值≠定义，我不动它")

type deviceRow struct {
	DeviceID        string `json:"device_id"`
	InstanceID      string `json:"instance_id"`
	InstanceState   string `json:"instance_state"`
	ConnectionState string `json:"connection_state"`
	ConnGeneration  int    `json:"conn_generation"`
	Environment     string `json:"environment"`
	Enterprise      string `json:"enterprise"`
	DeviceType      string `json:"device_type"`
	Overridden      bool   `json:"overridden"`
	Audio           struct {
		Format      string  `json:"format"`
		SampleRate  int     `json:"sample_rate"`
		BitrateKbps float64 `json:"bitrate_kbps"`
	} `json:"audio"`
}

type runResult struct {
	DeviceID        string `json:"device_id"`
	InstanceID      string `json:"instance_id"`
	TurnID          string `json:"turn_id"`
	Verdict         string `json:"verdict"`
	TurnEndReason   string `json:"turn_end_reason"`
	UplinkEndReason string `json:"uplink_end_reason"`
	ReplyKind       string `json:"reply_kind"`
	UpFormat        string `json:"up_format"`
	DownFormat      string `json:"down_format"`
	DownBytes       int    `json:"down_bytes"`
	Overridden      bool   `json:"overridden"`
}

func main() { os.Exit(simctl(os.Args[1:])) }

func simctl(args []string) int {
	cfg, listen, rest := peelGlobal(args)
	if len(rest) == 0 || rest[0] == "--help" || rest[0] == "-h" || rest[0] == "help" {
		fmt.Fprint(stderr, usage)
		if len(rest) == 0 {
			return 2
		}
		return 0
	}
	verb, rest := rest[0], rest[1:]
	switch verb {
	case "up":
		return cmdUp(cfg, listen, rest)
	case "down":
		return cmdDown(listen, rest)
	case "status":
		return cmdStatus(listen, rest)
	case "devices":
		return cmdDevices(listen, rest)
	case "assets":
		return cmdAssets(listen, rest)
	case "run":
		return cmdRun(listen, rest)
	case "turn":
		return cmdTurn(listen, rest)
	case "audio":
		return cmdAudio(listen, rest)
	case "history":
		return cmdHistory(listen, rest)
	default:
		return fail("未知动词 " + verb + "（simctl --help）")
	}
}

func peelGlobal(args []string) (cfg, listen string, rest []string) {
	cfg, listen = defaultConfig, defaultListen
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" && i+1 < len(args):
			cfg, i = args[i+1], i+1
		case strings.HasPrefix(a, "--config="):
			cfg = strings.TrimPrefix(a, "--config=")
		case a == "--listen" && i+1 < len(args):
			listen, i = args[i+1], i+1
		case strings.HasPrefix(a, "--listen="):
			listen = strings.TrimPrefix(a, "--listen=")
		default:
			return cfg, listen, args[i:]
		}
	}
	return cfg, listen, nil
}

func newFS(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseFS(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(flagsFirst(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

// flagsFirst 把位置参数挪到后面。stdlib flag 碰到第一个非 flag 就停，
// 但规格写法是 `simctl run sim_1 --asset ast_xxx`。
func flagsFirst(args []string) []string {
	boolFlag := map[string]bool{"parallel": true, "dirty": true, "help": true, "h": true}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "--"), "-")
		if boolFlag[name] {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func addListen(fs *flag.FlagSet, listen *string) {
	fs.StringVar(listen, "listen", *listen, "manager 地址")
}

func addConfig(fs *flag.FlagSet, cfg *string) {
	fs.StringVar(cfg, "config", *cfg, "manager YAML")
}

func addFilter(fs *flag.FlagSet, env, ent, dtype *string) {
	fs.StringVar(env, "env", "", "环境名")
	fs.StringVar(ent, "enterprise", "", "厂商简称")
	fs.StringVar(dtype, "device-type", "", "设备类型简称")
}

func out(v any) int {
	_ = json.NewEncoder(stdout).Encode(v)
	return 0
}

func fail(msg string) int {
	_ = json.NewEncoder(stdout).Encode(map[string]any{"error": msg})
	return 1
}

func client() *http.Client {
	if httpc != nil {
		return httpc
	}
	return http.DefaultClient
}

func httpRaw(method, listen, path string, in any) (int, []byte, http.Header, error) {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+listen+path, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header, err
}

func httpJSON(method, listen, path string, in, dst any) error {
	code, b, _, err := httpRaw(method, listen, path, in)
	if err != nil {
		return err
	}
	if code >= 400 {
		return errors.New(decodeErr(b, code))
	}
	if dst != nil && len(bytes.TrimSpace(b)) > 0 {
		return json.Unmarshal(b, dst)
	}
	return nil
}

func decodeErr(b []byte, code int) string {
	var m struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &m) == nil && m.Error != "" {
		return m.Error
	}
	return fmt.Sprintf("HTTP %d: %s", code, bytes.TrimSpace(b))
}

func httpGet(listen, path string, dst any) error {
	return httpJSON("GET", listen, path, nil, dst)
}

func httpPost(listen, path string, in, dst any) error {
	return httpJSON("POST", listen, path, in, dst)
}

func alive(listen string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+listen+"/devices", nil)
	if err != nil {
		return false
	}
	res, err := client().Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode == 200
}

func waitAlive(listen string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if alive(listen) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return alive(listen)
}

func readPID() (int, error) {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func detachCmd(cmd *exec.Cmd) {
	// CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW：simctl 退出后 manager 继续活，不弹控制台。
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x00000200 | 0x08000000,
	}
}

func killPID(pid int) error {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func cmdUp(cfg, listen string, args []string) int {
	fs := newFS("up")
	addConfig(fs, &cfg)
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	if alive(listen) {
		pid, _ := readPID()
		return out(map[string]any{"ok": true, "started": false, "pid": pid, "listen": listen})
	}
	if err := os.MkdirAll("data", 0o755); err != nil {
		return fail(err.Error())
	}
	build := exec.Command("go", "build", "-o", exePath, "./cmd/manager")
	build.Stdout, build.Stderr = stderr, stderr
	if err := build.Run(); err != nil {
		return fail("go build manager 失败: " + err.Error())
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err.Error())
	}
	defer logf.Close()
	cmd := exec.Command(exePath, "--config", cfg, "--listen", listen)
	cmd.Stdout, cmd.Stderr = logf, logf
	detachCmd(cmd)
	if err := cmd.Start(); err != nil {
		return fail("启动 manager 失败: " + err.Error())
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0o644)
	_ = cmd.Process.Release()
	if !waitAlive(listen, 15*time.Second) {
		return fail(fmt.Sprintf("manager pid=%d 起了但 GET /devices 不通，见 %s", pid, logPath))
	}
	return out(map[string]any{"ok": true, "started": true, "pid": pid, "listen": listen})
}

func cmdDown(listen string, args []string) int {
	fs := newFS("down")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	pid, err := readPID()
	if err != nil {
		if alive(listen) {
			return fail("manager 活着但没有 pid 文件，拒绝盲杀")
		}
		return out(map[string]any{"alive": false, "stopped": false})
	}
	_ = killPID(pid)
	_ = waitAliveDown(listen, 5*time.Second)
	_ = os.Remove(pidPath)
	if alive(listen) {
		return fail("manager 仍在听 " + listen)
	}
	return out(map[string]any{"alive": false, "stopped": true, "pid": pid})
}

func waitAliveDown(listen string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(listen) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !alive(listen)
}

func cmdStatus(listen string, args []string) int {
	fs := newFS("status")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	pid, _ := readPID()
	if !alive(listen) {
		return out(map[string]any{"alive": false, "listen": listen, "pid": pid})
	}
	var wrap struct {
		Devices []deviceRow `json:"devices"`
	}
	if err := httpGet(listen, "/devices", &wrap); err != nil {
		return fail(err.Error())
	}
	return out(map[string]any{"alive": true, "listen": listen, "pid": pid, "devices": len(wrap.Devices)})
}

func cmdDevices(listen string, args []string) int {
	fs := newFS("devices")
	var env, ent, dtype string
	addListen(fs, &listen)
	addFilter(fs, &env, &ent, &dtype)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	devs, err := listDevices(listen)
	if err != nil {
		return fail(err.Error())
	}
	return out(map[string]any{"devices": selectDevices(devs, fs.Arg(0), env, ent, dtype)})
}

func cmdAssets(listen string, args []string) int {
	fs := newFS("assets")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	var raw json.RawMessage
	if err := httpGet(listen, "/assets", &raw); err != nil {
		return fail(err.Error())
	}
	_, _ = stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}

func cmdRun(listen string, args []string) int {
	fs := newFS("run")
	var env, ent, dtype, asset string
	var parallel, dirty bool
	addListen(fs, &listen)
	addFilter(fs, &env, &ent, &dtype)
	fs.StringVar(&asset, "asset", "", "音频库资产 id")
	fs.BoolVar(&parallel, "parallel", false, "N 台并行")
	fs.BoolVar(&dirty, "dirty", false, "Running 且 overridden 时放行")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	if asset == "" {
		return fail("run 需要 --asset")
	}
	devs, err := listDevices(listen)
	if err != nil {
		return fail(err.Error())
	}
	selected := selectDevices(devs, fs.Arg(0), env, ent, dtype)
	if len(selected) == 0 {
		return fail("选中 0 台设备（--enterprise / --device-type 用简称不是名称；打错简称最常见）")
	}
	results := make([]any, len(selected))
	var failed int
	runOne := func(i int, d deviceRow) {
		r, err := runDevice(listen, d, asset, dirty)
		if err != nil {
			results[i] = map[string]any{"device_id": d.DeviceID, "error": err.Error()}
			return
		}
		results[i] = r
	}
	if parallel && len(selected) > 1 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		wg.Add(len(selected))
		for i, d := range selected {
			i, d := i, d
			go func() {
				defer wg.Done()
				runOne(i, d)
				if _, ok := results[i].(map[string]any); ok {
					mu.Lock()
					failed++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	} else {
		for i, d := range selected {
			runOne(i, d)
			if _, ok := results[i].(map[string]any); ok {
				failed++
			}
		}
	}
	_ = json.NewEncoder(stdout).Encode(results)
	if failed > 0 {
		return 1
	}
	return 0
}

func listDevices(listen string) ([]deviceRow, error) {
	var wrap struct {
		Devices []deviceRow `json:"devices"`
	}
	if err := httpGet(listen, "/devices", &wrap); err != nil {
		return nil, err
	}
	return wrap.Devices, nil
}

func selectDevices(all []deviceRow, id, env, ent, dtype string) []deviceRow {
	out := make([]deviceRow, 0, len(all))
	for _, d := range all {
		if id != "" && d.DeviceID != id {
			continue
		}
		if env != "" && d.Environment != env {
			continue
		}
		if ent != "" && d.Enterprise != ent {
			continue
		}
		if dtype != "" && d.DeviceType != dtype {
			continue
		}
		out = append(out, d)
	}
	return out
}

func runDevice(listen string, d deviceRow, asset string, dirty bool) (runResult, error) {
	ins, over, err := ensureReady(listen, d, dirty)
	if err != nil {
		return runResult{}, err
	}
	var speak struct {
		TurnID          string `json:"turn_id"`
		InstanceID      string `json:"instance_id"`
		TurnEndReason   string `json:"turn_end_reason"`
		UplinkEndReason string `json:"uplink_end_reason"`
		ReplyKind       string `json:"reply_kind"`
	}
	if err := httpPost(listen, "/devices/"+url.PathEscape(d.DeviceID)+"/speak_and_wait",
		map[string]any{"asset_id": asset}, &speak); err != nil {
		return runResult{}, err
	}
	if speak.InstanceID != "" {
		ins = speak.InstanceID
	}
	var turn struct {
		TurnID          string `json:"turn_id"`
		InstanceID      string `json:"instance_id"`
		TurnEndReason   string `json:"turn_end_reason"`
		UplinkEndReason string `json:"uplink_end_reason"`
		ReplyKind       string `json:"reply_kind"`
		UpFormat        string `json:"up_format"`
		DownFormat      string `json:"down_format"`
		DownBytes       int    `json:"down_bytes"`
	}
	q := "/devices/" + url.PathEscape(d.DeviceID) + "/turns/" + url.PathEscape(speak.TurnID) +
		"?instance_id=" + url.QueryEscape(ins)
	if err := httpGet(listen, q, &turn); err != nil {
		return runResult{}, err
	}
	end, kind := turn.TurnEndReason, turn.ReplyKind
	if end == "" {
		end = speak.TurnEndReason
	}
	if kind == "" {
		kind = speak.ReplyKind
	}
	upEnd := turn.UplinkEndReason
	if upEnd == "" {
		upEnd = speak.UplinkEndReason
	}
	return runResult{
		DeviceID:        d.DeviceID,
		InstanceID:      ins,
		TurnID:          speak.TurnID,
		Verdict:         Verdict(end, kind, turn.DownFormat, turn.DownBytes),
		TurnEndReason:   end,
		UplinkEndReason: upEnd,
		ReplyKind:       kind,
		UpFormat:        turn.UpFormat,
		DownFormat:      turn.DownFormat,
		DownBytes:       turn.DownBytes,
		Overridden:      over,
	}, nil
}

func ensureReady(listen string, d deviceRow, dirty bool) (instanceID string, overridden bool, err error) {
	over := d.Overridden
	idPath := "/devices/" + url.PathEscape(d.DeviceID)
	switch d.InstanceState {
	case "created", "stopped":
		if over {
			if err := httpPost(listen, idPath+"/config/reset", nil, nil); err != nil {
				return "", over, err
			}
			over = false
		}
		var st struct {
			InstanceID     string `json:"instance_id"`
			ConnGeneration int    `json:"conn_generation"`
		}
		if err := httpPost(listen, idPath+"/start", nil, &st); err != nil {
			return "", over, err
		}
		if st.InstanceID == "" {
			st.InstanceID = d.InstanceID
		}
		if err := httpPost(listen, idPath+"/wait_ready", map[string]any{
			"instance_id": st.InstanceID, "conn_generation": st.ConnGeneration,
		}, nil); err != nil {
			return "", over, err
		}
		return st.InstanceID, over, nil
	case "running":
		if over && !dirty {
			return "", over, errDirty
		}
		return d.InstanceID, over, nil
	case "starting":
		if over && !dirty {
			return "", over, errDirty
		}
		if err := httpPost(listen, idPath+"/wait_ready", map[string]any{
			"instance_id": d.InstanceID, "conn_generation": d.ConnGeneration,
		}, nil); err != nil {
			return "", over, err
		}
		return d.InstanceID, over, nil
	default:
		return "", over, fmt.Errorf("instance_state=%s，不能 run", d.InstanceState)
	}
}

func cmdTurn(listen string, args []string) int {
	fs := newFS("turn")
	var turnID, ins string
	addListen(fs, &listen)
	fs.StringVar(&turnID, "turn", "", "turn_id")
	fs.StringVar(&ins, "instance", "", "instance_id")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	dev := fs.Arg(0)
	if dev == "" || turnID == "" || ins == "" {
		return fail("turn 需要 device_id、--turn、--instance")
	}
	esc := url.PathEscape(dev)
	q := "?instance_id=" + url.QueryEscape(ins)
	var meta json.RawMessage
	if err := httpGet(listen, "/devices/"+esc+"/turns/"+url.PathEscape(turnID)+q, &meta); err != nil {
		return fail(err.Error())
	}
	var evWrap struct {
		Events []map[string]any `json:"events"`
		Source string           `json:"source"`
	}
	if err := httpGet(listen, "/devices/"+esc+"/events"+q, &evWrap); err != nil {
		return fail(err.Error())
	}
	filtered := make([]map[string]any, 0, len(evWrap.Events))
	for _, e := range evWrap.Events {
		if s, _ := e["turn_id"].(string); s == turnID {
			filtered = append(filtered, e)
		}
	}
	code, raw, _, err := httpRaw("GET", listen, "/devices/"+esc+"/turns/"+url.PathEscape(turnID)+"/frames"+q, nil)
	st := map[string]any{}
	if err != nil {
		st["error"] = err.Error()
	} else if code >= 400 {
		st["error"] = decodeErr(raw, code)
	} else {
		st = summarizeFrames(raw)
	}
	return out(map[string]any{
		"device_id":   dev,
		"instance_id": ins,
		"turn_id":     turnID,
		"source":      evWrap.Source,
		"turn":        json.RawMessage(meta),
		"events":      filtered,
		"frames":      st,
	})
}

func summarizeFrames(ndjson []byte) map[string]any {
	var lines, outb, inb, outn, inn int
	for _, line := range bytes.Split(ndjson, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row struct {
			Direction  string `json:"direction"`
			PayloadLen int    `json:"payload_len"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		lines++
		switch row.Direction {
		case "outbound":
			outn++
			outb += row.PayloadLen
		case "inbound":
			inn++
			inb += row.PayloadLen
		}
	}
	return map[string]any{
		"lines": lines, "outbound": outn, "inbound": inn,
		"outbound_bytes": outb, "inbound_bytes": inb,
	}
}

func cmdAudio(listen string, args []string) int {
	fs := newFS("audio")
	var turnID, ins, side, outFile string
	addListen(fs, &listen)
	fs.StringVar(&turnID, "turn", "", "turn_id")
	fs.StringVar(&ins, "instance", "", "instance_id")
	fs.StringVar(&side, "side", "", "uplink 或 downlink")
	fs.StringVar(&outFile, "out", "", "落盘路径")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	dev := fs.Arg(0)
	if dev == "" || turnID == "" || ins == "" || outFile == "" {
		return fail("audio 需要 device_id、--turn、--instance、--out")
	}
	if side != "uplink" && side != "downlink" {
		return fail("--side 必须是 uplink 或 downlink")
	}
	path := "/devices/" + url.PathEscape(dev) + "/turns/" + url.PathEscape(turnID) +
		"/audio/" + side + "?instance_id=" + url.QueryEscape(ins)
	code, raw, hdr, err := httpRaw("GET", listen, path, nil)
	if err != nil {
		return fail(err.Error())
	}
	if code >= 400 {
		return fail(decodeErr(raw, code))
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil && filepath.Dir(outFile) != "." {
		return fail(err.Error())
	}
	if err := os.WriteFile(outFile, raw, 0o644); err != nil {
		return fail(err.Error())
	}
	ct := ""
	if hdr != nil {
		ct = hdr.Get("Content-Type")
	}
	return out(map[string]any{
		"device_id": dev, "instance_id": ins, "turn_id": turnID,
		"side": side, "path": outFile, "bytes": len(raw), "content_type": ct,
	})
}

func cmdHistory(listen string, args []string) int {
	fs := newFS("history")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	dev := fs.Arg(0)
	if dev == "" {
		return fail("history 需要 device_id")
	}
	var raw json.RawMessage
	if err := httpGet(listen, "/devices/"+url.PathEscape(dev)+"/instances", &raw); err != nil {
		return fail(err.Error())
	}
	_, _ = stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}
