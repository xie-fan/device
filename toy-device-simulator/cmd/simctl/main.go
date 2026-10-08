package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultConfig   = "configs/manager.yaml"
	defaultListen   = "127.0.0.1:8090"
	defaultRegistry = "configs/registry.yaml"
	pidPath         = "data/manager.pid"
	exePath         = "data/manager.exe"
	logPath         = "data/manager.log"
	seedDevice      = "sim_0001"
)

const usage = `simctl — 对着本仓 manager 的任务级 CLI。一律 JSON 到 stdout，没有 --json 开关。

装好后（见 install）用 <skill 目录>/bin/simctl，从任何目录调用都以 skill 目录为家；
没装时在仓库 toy-device-simulator/ 下 go run ./cmd/simctl。人和 agent 共用同一个 manager。

用法:
  simctl [--config FILE] [--listen ADDR] <动词> [参数]

全局（任意动词前后都可写）:
  --config  manager YAML，默认 configs/manager.yaml；up 会原样传给 manager
  --listen  manager 地址，默认 127.0.0.1:8090（cmd/manager 的 --listen，不在 yaml 里）

动词:
  install   在仓库 toy-device-simulator/ 下跑一次：编好 simctl / manager / echosrv 放进
            skill 目录的 bin/（默认 ~/.agents/skills/simctl，--to 改），复制 skill 文档；
            configs/ 没有才放默认的；~/.claude/skills/simctl 不存在时链到安装目录。
            之后从任何目录用 <skill>/bin/simctl，它以 skill 目录为家：
            configs/、data/、recordings/ 都在那
  up        后台起 manager。装好的用 bin/manager；仓库里先 go build -o data/manager.exe ./cmd/manager。
            幂等，不自动关。pid → data/manager.pid，日志追加 data/manager.log。
            设备册为空时顺手建一台 sim_0001（输出 seeded_device）。
            探活 GET /devices。Windows 上 simctl 退出后进程继续活着。
            --registry FILE  配置树，默认 configs/registry.yaml。真实环境的 url
                             只能放 gitignore 的 configs/registry.local.yaml，
                             跟踪版只许 loopback。
  down      按 pid 文件停 manager
  status    manager 是否活着，几台设备
  context   送话前一次查齐：设备（id/状态/挂靠/租约）、挂靠三级树
            （环境名 → 厂商简称 → 类型简称 → 默认产品，空串 = 要带 --product）、
            产品 id、音频标签计数、音频集、图片资产
  devices   列设备。过滤：--env 环境名；--enterprise / --device-type 简称（不是名称）
  assets    列素材库（默认精简字段）。--tag X 按内容标签（对话/联网/唱歌…）；
            --kind audio|image；--full 原样输出全部字段
  audio-sets 列音频集（有序的一组音频，run --audio-set 整组送）
  products  列产品
  run       挑设备 → 挂靠 → 送话 → 读判语（核心）
            设备册条目只有 device_id，三级是「这次挂成什么」，必须给全：
            --env / --enterprise / --device-type 缺一个就报错。
              给了 device_id → 就那一台
              没给           → 从设备册随机挑（--count 决定几台）
            输出始终是数组：每台 × 每条音频一个元素
            --asset ID           素材库里的音频资产（--asset / --tag / --audio-set / --compose 四选一）
            --tag X              按内容标签挑第一条匹配的音频资产
            --compose A,B,C      几段拼成一条连续音频，一轮送出（一句话问几件事）：
                                 每项是 asset_id 或内容标签（取第一条），逗号/加号分隔；
                                 各段切掉首尾静音首尾相接，存成「组合」素材，同组合复用
            --audio-set S        音频集 id 或名称：每台按集内顺序逐条送完整组
                                 单条 400/404（素材本身的问题）记错接着送；其它错误
                                 这台停下，剩下的条目记「未跑」
            --image ID           图片资产：每轮先用本轮 UUID 传这张图再说话（带图送话），
                                 音频集的每条都带；服务端设备类型要开 imageChat
            --env NAME           环境名（挂靠，不是筛选）
            --enterprise SHORT   厂商简称（挂靠）
            --device-type SHORT  设备类型简称（挂靠）
            --product P          产品 id；不给则 start 不带，服务端用类型默认产品
            --set 路径=值        临时覆盖，可重复。值先按 JSON 解析，失败当字符串。
                                 带引号才是字符串（nic_iccid="8986"）
            --count N            没给 device_id 时随机挑几台，默认 1，0=整册全跑
            --parallel           多台时并行（默认串行）
            --dirty              在跑且覆盖≠这次 --set 时放行，否则报错不动它
            --force              抢占别的 run 的租约（确认那个 run 已经死了再用）
            --restart            在跑也先停机、清覆盖，再按这次的挂靠/产品/--set 起
                                 （换环境、换 --set 用它，不用先 stop）
            跑之前先 POST /devices/{id}/lease 占住，跑完还——两个并发 run 不会
            撞同一台。音频集每条之前续租。随机档撞上被占的会换下一台；
            跑失败不换台，故障照报。
            租约不挡人在调试台上的操作，只在 run 之间生效。
            Created/Stopped 且有覆盖时自动 POST /config/reset 再 start（无声）
            没启动就 start + wait_ready
            在跑：覆盖与这次 --set 不同才报脏；挂靠/产品/--set 静默不生效（--restart 例外）
            结果省掉空值字段；photo 只在这轮与拍照有关时出现
            换产品或改 ICCID 会让真实服务端重新校验这台设备
            结果多两项判读：hint_only=true 是 replied 但只收到断句提示音（没回答）；
            photo.result = ok / no_reply / not_uploaded / image_sent /
            no_command / skipped:原因，空 = 这轮与拍照无关
  stop      停一台设备（只停在跑的；先拿租约，别的 run 占着就报错）
            位置参数 device_id；--reset 停完清临时覆盖；--force 抢租约
  turn      一轮的事件流与帧统计（连续 tts_chunk 合成一条，带 count 与 payload_len 总和）
            位置参数 device_id；--turn ID --instance ID 必填
  audio     把上行或下行音频落到文件（stdout 仍是 JSON 指针）
            位置参数 device_id
            --turn ID --instance ID --side uplink|downlink --out FILE
  history   列这台设备的每一次运行（含跨重启）
            位置参数 device_id
  register  注册 App 账号（auth/otp + auth/account/Register），两步：
            第一步 --env E --account 邮箱或手机号 [--country CN]：发验证码，输出 request_id
                   测试邮箱 <任意>@test1.mail.anyonstack.com，验证码向用户要
            第二步再加 --code 验证码 --request-id R --password P：校验 + 注册，存 token
                   密码 8-16 位、至少两类字符
            配了 Cloud Mail 时邮箱一步走完：第一步带上 --password，自己去收件箱取码（等 90s）
                   SIMCTL_MAIL_URL 站点根；SIMCTL_MAIL_TOKEN，或 SIMCTL_MAIL_ADMIN + SIMCTL_MAIL_PASSWORD
  login     --env E --account A --password P（或 SIMCTL_APP_PASSWORD）：密码登录，
            token 按环境存到 data/app_tokens.json，bind / unbind 不带 --token 时用它
  bind      扮演手机 App 绑定：对设备所挂环境的 http_url 调 user/device/Bind，
            http_url 要和环境 url 同集群（ws://aichatbotws… → https://aichatbotwx…，路径照留）；
            设备 ready 却 2004、bind_received=false 多半是集群不对
            设备在线时模拟器自动回 bind/server code=0。位置参数 device_id
            --token T     App 登录态 Authorization，默认取 SIMCTL_APP_TOKEN，再退到 login 存的
            --identity ID 服务端要求身份时带 IdentityID
            --env / --enterprise / --device-type [--product]  设备没在跑时按这三级拉起来
            输出 code（0 成功；2004 设备没应答；14013 设备不存在；14014 厂商不一致；3101 token 无效）、
            bind_received（设备端收到下发没有）、in_list（Lists 里有没有它）
  unbind    同 bind 参数，调 user/device/UnBind
`

var (
	stdout io.Writer    = os.Stdout
	stderr io.Writer    = os.Stderr
	httpc  *http.Client // nil = http.DefaultClient
)

var errDirty = errors.New("这台在跑，身上的临时覆盖和这次 --set 不一致，我不动它")

type deviceRow struct {
	DeviceID        string         `json:"device_id"`
	InstanceID      string         `json:"instance_id"`
	InstanceState   string         `json:"instance_state"`
	ConnectionState string         `json:"connection_state"`
	ConnGeneration  int            `json:"conn_generation"`
	Environment     string         `json:"environment"`
	Enterprise      string         `json:"enterprise"`
	DeviceType      string         `json:"device_type"`
	Overridden      bool           `json:"overridden"`
	Product         string         `json:"product"`
	Overrides       map[string]any `json:"overrides"`
	// 被别的 run 占着时才有值。devices 动词要能答「为什么我的 run 说全被占了」。
	LeasedUntil string `json:"leased_until,omitempty"`
	LeaseOwner  string `json:"lease_owner,omitempty"`
	Audio       struct {
		Format      string  `json:"format"`
		SampleRate  int     `json:"sample_rate"`
		BitrateKbps float64 `json:"bitrate_kbps"`
	} `json:"audio"`
}

type runResult struct {
	DeviceID        string         `json:"device_id"`
	AssetID         string         `json:"asset_id"`
	ImageAssetID    string         `json:"image_asset_id,omitempty"`
	InstanceID      string         `json:"instance_id"`
	TurnID          string         `json:"turn_id"`
	Verdict         string         `json:"verdict"`
	TurnEndReason   string         `json:"turn_end_reason"`
	UplinkEndReason string         `json:"uplink_end_reason,omitempty"`
	ReplyKind       string         `json:"reply_kind,omitempty"`
	UpFormat        string         `json:"up_format,omitempty"`
	DownFormat      string         `json:"down_format,omitempty"`
	DownBytes       int            `json:"down_bytes"`
	Overridden      bool           `json:"overridden,omitempty"`
	Product         string         `json:"product,omitempty"`
	Overrides       map[string]any `json:"overrides,omitempty"`
	HintOnly        bool           `json:"hint_only,omitempty"`
	// 这轮与拍照无关（三项事实全空、result 也空）时整个省掉。
	Photo *photoSummary `json:"photo,omitempty"`
}

type photoSummary struct {
	Command  bool   `json:"command"`
	Uploaded bool   `json:"uploaded"`
	Skipped  string `json:"skipped"`
	Result   string `json:"result,omitempty"`
}

func main() {
	enterHome()
	os.Exit(simctl(os.Args[1:]))
}

var (
	callerWD    string // 调用时的当前目录：audio --out 的相对路径按它算
	homeManager string // 装好的 skill 里 bin/ 下的 manager；空 = 仓库里，up 现编
)

// enterHome：装进 skill 后 simctl 在 <skill>/bin/ 里，以 <skill> 为家——configs/、data/、
// recordings/ 都在那，从哪个目录调用都一样。仓库里 go run 时可执行文件在临时目录，不切。
func enterHome() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	bin := filepath.Dir(exe)
	if filepath.Base(bin) != "bin" {
		return
	}
	callerWD, _ = os.Getwd()
	if err := os.Chdir(filepath.Dir(bin)); err != nil {
		return
	}
	if m := filepath.Join(bin, "manager"+exeExt()); fileExists(m) {
		homeManager = m
	}
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

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
	case "install":
		return cmdInstall(rest)
	case "up":
		return cmdUp(cfg, listen, rest)
	case "down":
		return cmdDown(listen, rest)
	case "status":
		return cmdStatus(listen, rest)
	case "devices":
		return cmdDevices(listen, rest)
	case "context":
		return cmdContext(listen, rest)
	case "assets":
		return cmdAssets(listen, rest)
	case "audio-sets":
		return cmdAudioSets(listen, rest)
	case "products":
		return cmdProducts(listen, rest)
	case "run":
		return cmdRun(listen, rest)
	case "stop":
		return cmdStop(listen, rest)
	case "turn":
		return cmdTurn(listen, rest)
	case "audio":
		return cmdAudio(listen, rest)
	case "history":
		return cmdHistory(listen, rest)
	case "register":
		return cmdRegister(listen, rest)
	case "login":
		return cmdLogin(listen, rest)
	case "bind":
		return cmdBind(listen, rest, false)
	case "unbind":
		return cmdBind(listen, rest, true)
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
	boolFlag := map[string]bool{"parallel": true, "dirty": true, "force": true, "full": true, "restart": true, "reset": true, "help": true, "h": true}
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
	var registry string
	addConfig(fs, &cfg)
	addListen(fs, &listen)
	// 真实环境的 url 只能待在 gitignore 的 registry.local.yaml 里——跟踪版
	// 只许 loopback（config 包的守卫测试会挡）。不给这个开关，界面上加的
	// 真环境就只能写进跟踪版，等于逼着人违规。
	fs.StringVar(&registry, "registry", defaultRegistry, "配置树 YAML，原样传给 manager")
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
	mgr := homeManager
	if mgr == "" { // 仓库里：现编
		mgr = exePath
		build := exec.Command("go", "build", "-o", exePath, "./cmd/manager")
		build.Stdout, build.Stderr = stderr, stderr
		if err := build.Run(); err != nil {
			return fail("go build manager 失败: " + err.Error())
		}
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err.Error())
	}
	defer logf.Close()
	cmd := exec.Command(mgr, "--config", cfg, "--listen", listen, "--registry", registry)
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
	res := map[string]any{"ok": true, "started": true, "pid": pid, "listen": listen}
	// 新环境拉下仓库，设备册（data/devices.yaml，不入库）是空的，run 挑不到设备。
	// 建一台 sim_0001 就能跑：身份字段来自默认产品，不是哪台真机。
	if devs, err := listDevices(listen); err == nil && len(devs) == 0 {
		if err := httpPost(listen, "/devices", map[string]any{"device_id": seedDevice}, nil); err == nil {
			res["seeded_device"] = seedDevice
		}
	}
	return out(res)
}

// cmdInstall 在仓库的 toy-device-simulator/ 下跑：把 skill/（源文件）装成一个自带程序的目录。
// bin/ 放编好的 simctl、manager、echosrv；SKILL.md 与 references/ 每次覆盖成仓库版本；
// configs/ 只在没有时放默认的——装好后它和 data/、recordings/ 都是本机自己的，重装不动。
func cmdInstall(args []string) int {
	fs := newFS("install")
	home, _ := os.UserHomeDir()
	to := filepath.Join(home, ".agents", "skills", "simctl")
	fs.StringVar(&to, "to", to, "安装目录")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	if !fileExists("cmd/simctl") {
		return fail("install 要在仓库的 toy-device-simulator/ 目录下跑")
	}
	for _, c := range []string{"simctl", "manager", "echosrv"} {
		b := exec.Command("go", "build", "-o", filepath.Join(to, "bin", c+exeExt()), "./cmd/"+c)
		b.Stdout, b.Stderr = stderr, stderr
		if err := b.Run(); err != nil {
			return fail("编译 " + c + " 失败（装好的 manager 在跑会占住文件，先 simctl down）：" + err.Error())
		}
	}
	if err := copyTree("skill", to); err != nil {
		return fail("复制 skill 文档失败：" + err.Error())
	}
	var kept []string
	for _, f := range []string{"manager.yaml", "registry.yaml"} {
		dst := filepath.Join(to, "configs", f)
		if fileExists(dst) {
			kept = append(kept, dst)
			continue
		}
		if err := copyFile(filepath.Join("configs", f), dst); err != nil {
			return fail(err.Error())
		}
	}
	// ~/.claude/skills/simctl 链到安装目录：两边是同一份，数据不分家。已存在就不动。
	link := filepath.Join(home, ".claude", "skills", "simctl")
	linkNote := "已存在，没动"
	if filepath.Clean(link) == filepath.Clean(to) {
		linkNote = "就是安装目录"
	} else if _, err := os.Lstat(link); os.IsNotExist(err) {
		if err := makeDirLink(link, to); err != nil {
			linkNote = "建链接失败：" + err.Error()
		} else {
			linkNote = "已链到安装目录"
		}
	}
	return out(map[string]any{
		"installed": to, "simctl": filepath.Join(to, "bin", "simctl"+exeExt()), "configs_kept": kept,
		"claude_link": link, "claude_link_note": linkNote,
	})
}

// makeDirLink：Windows 建目录联接（不要管理员权限），其它系统建符号链接。
func makeDirLink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
	}
	return os.Symlink(target, link)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		return copyFile(p, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
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
	var tag, kind string
	var full bool
	fs.StringVar(&tag, "tag", "", "按内容标签过滤")
	fs.StringVar(&kind, "kind", "", "audio 或 image")
	fs.BoolVar(&full, "full", false, "原样输出全部字段")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	u := "/assets"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if full {
		return printGet(listen, u)
	}
	// 默认只给挑素材用得上的字段：码率、声道、epoch 这些占了大半体积，agent 用不着。
	var raw struct {
		Assets []briefAsset `json:"assets"`
	}
	if err := httpGet(listen, u, &raw); err != nil {
		return fail(err.Error())
	}
	return out(raw)
}

type briefAsset struct {
	AssetID    string   `json:"asset_id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Tags       []string `json:"tags,omitempty"`
	DurationMs int      `json:"duration_ms,omitempty"`
}

// cmdContext 一次给齐送话前要查的东西：manager、设备、挂靠三级、产品、素材标签、
// 音频集、图片。替代 status + devices + products + assets + audio-sets + GET /registry。
func cmdContext(listen string, args []string) int {
	fs := newFS("context")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	pid, _ := readPID()
	if !alive(listen) {
		return out(map[string]any{"alive": false, "listen": listen, "pid": pid})
	}
	devs, err := listDevices(listen)
	if err != nil {
		return fail(err.Error())
	}
	type briefDev struct {
		DeviceID   string `json:"device_id"`
		State      string `json:"state"`
		Env        string `json:"env,omitempty"`
		Enterprise string `json:"enterprise,omitempty"`
		DeviceType string `json:"device_type,omitempty"`
		Product    string `json:"product,omitempty"`
		Overridden bool   `json:"overridden,omitempty"`
		LeaseOwner string `json:"lease_owner,omitempty"`
	}
	bd := make([]briefDev, 0, len(devs))
	for _, d := range devs {
		bd = append(bd, briefDev{d.DeviceID, d.InstanceState, d.Environment, d.Enterprise,
			d.DeviceType, d.Product, d.Overridden, d.LeaseOwner})
	}
	// 环境名 → 厂商简称 → 类型简称 → 默认产品（空串 = 没配，run 要带 --product）。
	var reg struct {
		Environments []struct {
			Name        string `json:"name"`
			Enterprises []struct {
				ShortName   string `json:"short_name"`
				DeviceTypes []struct {
					ShortName      string `json:"short_name"`
					DefaultProduct string `json:"default_product"`
				} `json:"device_types"`
			} `json:"enterprises"`
		} `json:"environments"`
	}
	tree := map[string]map[string]map[string]string{}
	if httpGet(listen, "/registry", &reg) == nil {
		for _, e := range reg.Environments {
			ents := map[string]map[string]string{}
			for _, ent := range e.Enterprises {
				types := map[string]string{}
				for _, t := range ent.DeviceTypes {
					types[t.ShortName] = t.DefaultProduct
				}
				ents[ent.ShortName] = types
			}
			tree[e.Name] = ents
		}
	}
	var prods struct {
		Products []struct {
			ID string `json:"id"`
		} `json:"products"`
	}
	_ = httpGet(listen, "/products", &prods)
	pids := []string{}
	for _, p := range prods.Products {
		pids = append(pids, p.ID)
	}
	var assets struct {
		Assets []briefAsset `json:"assets"`
	}
	if err := httpGet(listen, "/assets", &assets); err != nil {
		return fail(err.Error())
	}
	tags, untagged := map[string]int{}, 0
	images := []map[string]string{}
	for _, a := range assets.Assets {
		if a.Kind == "image" {
			images = append(images, map[string]string{"asset_id": a.AssetID, "name": a.Name})
			continue
		}
		if len(a.Tags) == 0 {
			untagged++
		}
		for _, t := range a.Tags {
			tags[t]++
		}
	}
	var sets struct {
		AudioSets []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			AssetIDs []string `json:"asset_ids"`
		} `json:"audio_sets"`
	}
	_ = httpGet(listen, "/audio_sets", &sets)
	bs := []map[string]any{}
	for _, s := range sets.AudioSets {
		bs = append(bs, map[string]any{"id": s.ID, "name": s.Name, "count": len(s.AssetIDs)})
	}
	return out(map[string]any{
		"alive": true, "listen": listen, "pid": pid,
		"devices": bd, "registry": tree, "products": pids,
		"audio_tags": tags, "audio_untagged": untagged,
		"audio_sets": bs, "images": images,
	})
}

// printGet 原样转发一个 GET 的 JSON 到 stdout。
func printGet(listen, path string) int {
	var raw json.RawMessage
	if err := httpGet(listen, path, &raw); err != nil {
		return fail(err.Error())
	}
	_, _ = stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return 0
}

func cmdAudioSets(listen string, args []string) int {
	fs := newFS("audio-sets")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	return printGet(listen, "/audio_sets")
}

// resolveAudioSet 把音频集 id 或名称解析成集内的 asset_id 列表（保持集内顺序）。
// 名称在服务端唯一，id 带 set_ 前缀，两者不会撞。
func resolveAudioSet(listen, key string) ([]string, error) {
	var raw struct {
		AudioSets []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			AssetIDs []string `json:"asset_ids"`
		} `json:"audio_sets"`
	}
	if err := httpGet(listen, "/audio_sets", &raw); err != nil {
		return nil, err
	}
	for _, set := range raw.AudioSets {
		if set.ID != key && set.Name != key {
			continue
		}
		if len(set.AssetIDs) == 0 {
			return nil, fmt.Errorf("音频集 %q 是空的", key)
		}
		return set.AssetIDs, nil
	}
	return nil, fmt.Errorf("没有音频集 %q（audio-sets 可核对）", key)
}

// resolveAssetByTag 把内容标签解析成 asset_id：GET /assets?tag=&kind=audio，
// 取列表第一条（按 createdAt 排序，结果可复现）。
func resolveAssetByTag(listen, tag string) (string, error) {
	var raw struct {
		Assets []struct {
			AssetID string `json:"asset_id"`
		} `json:"assets"`
	}
	u := "/assets?kind=audio&tag=" + url.QueryEscape(tag)
	if err := httpGet(listen, u, &raw); err != nil {
		return "", err
	}
	if len(raw.Assets) == 0 {
		return "", fmt.Errorf("素材库里没有 tag=%q 的音频（assets --tag %s 可核对）", tag, tag)
	}
	return raw.Assets[0].AssetID, nil
}

// resolveCompose 把「你好,联网,ast_xxx」解析成来源列表（ast_ 开头是 asset_id，否则按
// 内容标签取第一条），交给 POST /assets/compose 拼成一条连续音频。同组合服务端复用。
func resolveCompose(listen, spec string) (string, error) {
	parts := strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == '，' || r == '+' })
	ids := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "ast_") {
			ids = append(ids, p)
			continue
		}
		id, err := resolveAssetByTag(listen, p)
		if err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	var a struct {
		AssetID string `json:"asset_id"`
	}
	if err := httpPost(listen, "/assets/compose", map[string]any{"asset_ids": ids}, &a); err != nil {
		return "", fmt.Errorf("拼接失败：%w", err)
	}
	return a.AssetID, nil
}

func cmdProducts(listen string, args []string) int {
	fs := newFS("products")
	addListen(fs, &listen)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	return printGet(listen, "/products")
}

// setFlag 收集可重复的 --set。
type setFlag []string

func (s *setFlag) String() string { return strings.Join(*s, ", ") }
func (s *setFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseSets 只按第一个 = 切。值先按 JSON 解析，失败当字符串。
func parseSets(vals []string) (map[string]any, error) {
	out := make(map[string]any, len(vals))
	for _, v := range vals {
		path, val, ok := strings.Cut(v, "=")
		if !ok || path == "" {
			return nil, fmt.Errorf("--set 要 路径=值，得到 %q", v)
		}
		var parsed any
		if json.Unmarshal([]byte(val), &parsed) != nil {
			parsed = val
		}
		out[path] = parsed
	}
	return out, nil
}

func cmdRun(listen string, args []string) int {
	fs := newFS("run")
	var env, ent, dtype, asset, product, tag, audioSet, image, compose string
	var sets setFlag
	var count int
	var parallel, dirty, force, restart bool
	addListen(fs, &listen)
	addFilter(fs, &env, &ent, &dtype)
	fs.StringVar(&asset, "asset", "", "音频库资产 id")
	fs.StringVar(&tag, "tag", "", "按内容标签挑第一条匹配的音频资产")
	fs.StringVar(&audioSet, "audio-set", "", "音频集 id 或名称：按集内顺序逐条送")
	fs.StringVar(&compose, "compose", "", "几段拼成一条连续音频送：asset_id 或内容标签，逗号/加号分隔")
	fs.StringVar(&product, "product", "", "产品 id")
	fs.StringVar(&image, "image", "", "图片资产 id：每轮先传这张图再说话（带图送话）")
	fs.Var(&sets, "set", "临时覆盖 路径=值，可重复")
	fs.IntVar(&count, "count", 1, "随机挑几台（0=全部）")
	fs.BoolVar(&parallel, "parallel", false, "N 台并行")
	fs.BoolVar(&dirty, "dirty", false, "在跑且覆盖≠这次 --set 时放行")
	fs.BoolVar(&force, "force", false, "抢占别的 run 的租约")
	fs.BoolVar(&restart, "restart", false, "在跑也先停机、清覆盖再按这次参数起")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	picked := 0
	for _, v := range []string{asset, tag, audioSet, compose} {
		if v != "" {
			picked++
		}
	}
	if picked != 1 {
		return fail("run 需要 --asset / --tag / --audio-set / --compose 四选一")
	}
	assets := []string{asset}
	switch {
	case tag != "":
		id, err := resolveAssetByTag(listen, tag)
		if err != nil {
			return fail(err.Error())
		}
		assets = []string{id}
	case audioSet != "":
		ids, err := resolveAudioSet(listen, audioSet)
		if err != nil {
			return fail(err.Error())
		}
		assets = ids
	case compose != "":
		id, err := resolveCompose(listen, compose)
		if err != nil {
			return fail(err.Error())
		}
		assets = []string{id}
	}
	// Phase 11：三级不再是筛设备的条件，而是「这次挂成什么」，必须给全。
	if env == "" || ent == "" || dtype == "" {
		return fail("run 需要 --env / --enterprise / --device-type 三级给全（挂靠，不是筛选；简称不是名称）")
	}
	overrides, err := parseSets(sets)
	if err != nil {
		return fail(err.Error())
	}
	b := bind{Env: env, Ent: ent, Typ: dtype, Product: product, Overrides: overrides, Image: image, Restart: restart}
	devs, err := listDevices(listen)
	if err != nil {
		return fail(err.Error())
	}
	// 设备册条目不带三级，所以只按 device_id 挑；给了就是那一台，没给就整册随机。
	var selected []deviceRow
	if id := fs.Arg(0); id != "" {
		for _, d := range devs {
			if d.DeviceID == id {
				selected = append(selected, d)
			}
		}
		if len(selected) == 0 {
			return fail("设备册里没有 " + id)
		}
	} else {
		if len(devs) == 0 {
			return fail("设备册是空的，先建一台设备")
		}
		if count == 1 {
			return runRandomOne(listen, shuffled(devs), b, assets, dirty, force)
		}
		selected = shuffled(devs)
		if count > 0 && count < len(selected) {
			selected = selected[:count]
		}
	}
	// 被占的不跳过：它的每一条都是带 error 的元素。数组要和「选中集 × 条目」一一对应，
	// 否则读的人分不清「这台没跑」和「这台跑了没回话」。
	results := make([][]any, len(selected))
	if parallel && len(selected) > 1 {
		var wg sync.WaitGroup
		for i, d := range selected {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], _ = runOneLeased(listen, d, b, assets, dirty, force)
			}()
		}
		wg.Wait()
	} else {
		for i, d := range selected {
			results[i], _ = runOneLeased(listen, d, b, assets, dirty, force)
		}
	}
	var flat []any
	for _, r := range results {
		flat = append(flat, r...)
	}
	return emit(flat)
}

// emit 打印结果数组。退出码只说调用链：有 error 元素就是 1，判语不参与。
func emit(res []any) int {
	_ = json.NewEncoder(stdout).Encode(res)
	for _, r := range res {
		if _, ok := r.(map[string]any); ok {
			return 1
		}
	}
	return 0
}

// runRandomOne 洗牌后逐台试租，第一台租到的就是它。
//
// 纪律：只对争用跳台，绝不对失败跳台。一台设备真起不来就该把那个错报出来——
// 安静换一台跑成功会把故障藏起来，--dirty 门禁和 last_error 那两个诊断全白费。
func runRandomOne(listen string, order []deviceRow, b bind, assets []string, dirty, force bool) int {
	for _, d := range order {
		res, busy := runOneLeased(listen, d, b, assets, dirty, force)
		if busy {
			continue
		}
		return emit(res)
	}
	return fail(fmt.Sprintf("命中 %d 台，全部被别的 run 占着（lease_held）；确认那些 run 已经死了可加 --force 抢占", len(order)))
}

// shuffled 用 math/rand/v2：挑设备要的是分散不是不可预测。ID 生成那边继续用
// crypto/rand，两者用途不同，别混。
func shuffled(in []deviceRow) []deviceRow {
	out := append([]deviceRow(nil), in...)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

type leaseHandle struct {
	ID     string    `json:"lease_id"`
	Device deviceRow `json:"device"`
}

// runOneLeased 先租再跑，跑完必还。assets 按顺序逐条送（单条 --asset 就是长度 1）。
// 返回的切片与 assets 一一对应：跑了的是 runResult，出错或没跑到的是带 error 的 map。
// busy=true 表示被别的 run 占着——随机档据此换台，批量档照单收下那些 error 元素。
func runOneLeased(listen string, d deviceRow, b bind, assets []string, dirty, force bool) ([]any, bool) {
	l, busy, err := tryLease(listen, d.DeviceID, force)
	if busy || err != nil {
		return notRun(d.DeviceID, assets, err), busy
	}
	leaseID := l.ID
	// 释放失败不看：manager 挂了、网断了都有 TTL 兜底，多写一个分支是纯负债。
	// 闭包读最新的 id：续租碰上已过期会换新 id，defer 时就把 id 定死会还错。
	defer func() { releaseLease(listen, d.DeviceID, leaseID) }()
	// 用租约回的新鲜行，不用列表里那份——列表到 start 之间设备状态可能已经变了。
	row := l.Device
	if row.DeviceID == "" {
		row = d
	}
	ins, over, err := ensureReady(listen, row, b, dirty)
	if err != nil {
		return notRun(d.DeviceID, assets, annotateNotReady(listen, d.DeviceID, err)), false
	}
	// 产品与覆盖一台读一次：一组送话之间不会变。失败保持零值，不让 run 失败——既有桩没有这个路由。
	// overridden 也取设备当前值，和 overrides 对得上；读不到才退回 start 前的状态。
	var snap struct {
		Product    string         `json:"product"`
		Overrides  map[string]any `json:"overrides"`
		Overridden bool           `json:"overridden"`
	}
	if httpGet(listen, "/devices/"+url.PathEscape(d.DeviceID), &snap) == nil {
		over = snap.Overridden
	}
	var cfg struct {
		Features struct {
			Photo struct {
				Enabled bool `json:"enabled"`
			} `json:"photo"`
		} `json:"features"`
	}
	_ = httpGet(listen, "/devices/"+url.PathEscape(d.DeviceID)+"/config", &cfg)
	out := make([]any, 0, len(assets))
	for i, asset := range assets {
		if i > 0 {
			id, err := renewLease(listen, d.DeviceID, leaseID)
			if err != nil {
				return append(out, notRun(d.DeviceID, assets[i:], err)...), false
			}
			leaseID = id
		}
		r, fatal, err := speakOnce(listen, d.DeviceID, ins, asset, b.Image)
		if err != nil {
			out = append(out, map[string]any{"device_id": d.DeviceID, "asset_id": asset, "error": err.Error()})
			if fatal {
				return append(out, notRun(d.DeviceID, assets[i+1:], err)...), false
			}
			continue
		}
		r.Overridden, r.Product, r.Overrides = over, snap.Product, snap.Overrides
		r.Photo.Result = photoResult(*r.Photo, r.ReplyKind, r.Verdict, r.HintOnly, cfg.Features.Photo.Enabled)
		if *r.Photo == (photoSummary{}) {
			r.Photo = nil
		}
		out = append(out, r)
	}
	return out, false
}

// notRun 给没跑到的条目占位，数组长度才能恒等于「设备 × 条目」。
func notRun(dev string, assets []string, cause error) []any {
	out := make([]any, 0, len(assets))
	for _, a := range assets {
		out = append(out, map[string]any{"device_id": dev, "asset_id": a, "error": "未跑：" + cause.Error()})
	}
	return out
}

// renewLease 续租：带上现任 lease_id 再 POST 一次。绝不带 steal——那是抢，不是续。
// 已过期的服务端会发新 id，以返回值为准。
func renewLease(listen, dev, leaseID string) (string, error) {
	var l leaseHandle
	if err := httpPost(listen, "/devices/"+dev+"/lease",
		map[string]any{"owner": leaseOwner(), "lease_id": leaseID}, &l); err != nil {
		return "", fmt.Errorf("续租失败（租约被别的 run 拿走了？）：%w", err)
	}
	return l.ID, nil
}

func tryLease(listen, id string, steal bool) (leaseHandle, bool, error) {
	var l leaseHandle
	code, b, _, err := httpRaw("POST", listen, "/devices/"+id+"/lease",
		map[string]any{"owner": leaseOwner(), "steal": steal})
	if err != nil {
		return l, false, err
	}
	if code == http.StatusConflict {
		msg := decodeErr(b, code)
		var h struct {
			Owner   string `json:"owner"`
			Expires string `json:"expires_at"`
		}
		if json.Unmarshal(b, &h) == nil && h.Owner != "" {
			msg += fmt.Sprintf("（%s 占着，到期 %s）", h.Owner, h.Expires)
		}
		return l, true, errors.New(msg)
	}
	if code >= 400 {
		return l, false, errors.New(decodeErr(b, code))
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return l, false, err
	}
	return l, false, nil
}

func releaseLease(listen, id, leaseID string) {
	if leaseID == "" {
		return
	}
	_, _, _, _ = httpRaw("DELETE", listen, "/devices/"+id+"/lease?lease_id="+url.QueryEscape(leaseID), nil)
}

// leaseOwner 只为让 409 的报错说得出「被谁占着」，不是身份凭证。
func leaseOwner() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("simctl@%s/%d", host, os.Getpid())
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

// speakOnce 送一条、读判语。fatal=true 表示这台已经送不动了（没连着、槽被占、
// 等不到终态……），后面的条目不必再试；400/404 是这条音频自己的问题（库里没有、
// 是图片、转不了码），接着送下一条。
func speakOnce(listen, dev, ins, asset, image string) (runResult, bool, error) {
	body := map[string]any{"asset_id": asset}
	if image != "" {
		body["image_asset_id"] = image
	}
	code, raw, _, err := httpRaw("POST", listen, "/devices/"+url.PathEscape(dev)+"/speak_and_wait", body)
	if err != nil {
		return runResult{}, true, err
	}
	var speak struct {
		TurnID          string `json:"turn_id"`
		InstanceID      string `json:"instance_id"`
		TurnEndReason   string `json:"turn_end_reason"`
		UplinkEndReason string `json:"uplink_end_reason"`
		ReplyKind       string `json:"reply_kind"`
	}
	_ = json.Unmarshal(raw, &speak)
	if code >= 400 {
		msg := decodeErr(raw, code)
		if speak.TurnID != "" {
			// 504 等不到终态不等于没送出：带上这一轮，好直接下钻。
			msg += fmt.Sprintf("（turn_id %s，instance_id %s）", speak.TurnID, speak.InstanceID)
		}
		return runResult{}, code != http.StatusBadRequest && code != http.StatusNotFound, errors.New(msg)
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
	q := "/devices/" + url.PathEscape(dev) + "/turns/" + url.PathEscape(speak.TurnID) +
		"?instance_id=" + url.QueryEscape(ins)
	if err := httpGet(listen, q, &turn); err != nil {
		return runResult{}, false, err // 这条送出去了，只是读回失败；设备本身没坏
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
	r := runResult{
		DeviceID:        dev,
		AssetID:         asset,
		ImageAssetID:    image,
		InstanceID:      ins,
		TurnID:          speak.TurnID,
		Verdict:         Verdict(end, kind, turn.DownFormat, turn.DownBytes),
		TurnEndReason:   end,
		UplinkEndReason: upEnd,
		ReplyKind:       kind,
		UpFormat:        turn.UpFormat,
		DownFormat:      turn.DownFormat,
		DownBytes:       turn.DownBytes,
		Photo:           &photoSummary{},
	}
	// 失败保持零值，不让 run 失败。既有桩没有这个路由。
	var evWrap struct {
		Events []map[string]any `json:"events"`
	}
	eq := "/devices/" + url.PathEscape(dev) + "/events?instance_id=" + url.QueryEscape(ins)
	chunks := 0
	if httpGet(listen, eq, &evWrap) == nil {
		for _, e := range evWrap.Events {
			if s, _ := e["turn_id"].(string); s != speak.TurnID {
				continue
			}
			switch e["event_type"] {
			case "tts_chunk":
				chunks++
			case "photo_command":
				r.Photo.Command = true
			case "photo_uploaded":
				r.Photo.Uploaded = true
			case "photo_skipped":
				r.Photo.Skipped, _ = e["reason"].(string)
			}
		}
	}
	r.HintOnly = hintOnly(r.Verdict, chunks, r.DownBytes)
	return r, false, nil
}

// annotateNotReady 给起不来的错误补上原因。`generation_gone` 这类错误自己说不出
// 连接为什么没成——答案在设备的 last_error 里（「register ACK 超时」之类）。
// 不补的话 agent 只能拿着一个空洞的错误码，还得自己知道去翻 REST。
func annotateNotReady(listen, id string, err error) error {
	if errors.Is(err, errDirty) {
		return err // 门禁拒绝，不是连接问题
	}
	var row struct {
		LastError string `json:"last_error"`
	}
	if e := httpGet(listen, "/devices/"+url.PathEscape(id), &row); e != nil || row.LastError == "" {
		return err
	}
	return fmt.Errorf("%w（last_error: %s）", err, row.LastError)
}

// bind 是这次 run 的挂靠三级，外加可选产品和临时覆盖（start 时带上）。
type bind struct {
	Env, Ent, Typ string
	Product       string
	Overrides     map[string]any
	Image         string // --image：每轮都带的图（phase14），不参与 start
	Restart       bool   // --restart：在跑也先停机、清覆盖，再按这次的挂靠/产品/--set 起
}

// overridesEqual：nil 与空 map 相等。JSON 数字是 float64，与 parseSets 一致。
func overridesEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || reflect.DeepEqual(a, b)
}

// dirtyBlocked 在跑的设备要不要拦。没覆盖：静默复用。有覆盖且 ≠ 这次 --set：拦。
// Overridden 但覆盖 map 空、--set 也空：当「当前值≠定义」，沿用既有门禁。
func dirtyBlocked(d deviceRow, sets map[string]any, dirty bool) bool {
	if dirty {
		return false
	}
	if !d.Overridden && len(d.Overrides) == 0 {
		return false
	}
	if !overridesEqual(d.Overrides, sets) {
		return true
	}
	return d.Overridden && len(d.Overrides) == 0 && len(sets) == 0
}

func ensureReady(listen string, d deviceRow, b bind, dirty bool) (instanceID string, overridden bool, err error) {
	over := d.Overridden
	idPath := "/devices/" + url.PathEscape(d.DeviceID)
	if b.Restart && (d.InstanceState == "running" || d.InstanceState == "starting") {
		if err := httpPost(listen, idPath+"/stop", nil, nil); err != nil {
			return "", over, err
		}
		d.InstanceState = "stopped" // 下面的 stopped 分支负责 reset + start
	}
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
		body := map[string]any{
			"environment": b.Env, "enterprise": b.Ent, "device_type": b.Typ,
		}
		if b.Product != "" {
			body["product"] = b.Product
		}
		if len(b.Overrides) > 0 {
			body["overrides"] = b.Overrides
		}
		if err := httpPost(listen, idPath+"/start", body, &st); err != nil {
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
		if dirtyBlocked(d, b.Overrides, dirty) {
			return "", over, errDirty
		}
		return d.InstanceID, over, nil
	case "starting":
		if dirtyBlocked(d, b.Overrides, dirty) {
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

// cmdStop 停一台设备，--reset 顺带清掉临时覆盖。先拿租约：别的 run 正用着就不停它。
func cmdStop(listen string, args []string) int {
	fs := newFS("stop")
	var reset, force bool
	addListen(fs, &listen)
	fs.BoolVar(&reset, "reset", false, "停完再清临时覆盖")
	fs.BoolVar(&force, "force", false, "抢占别的 run 的租约")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	dev := fs.Arg(0)
	if dev == "" {
		return fail("stop 需要 device_id")
	}
	l, _, err := tryLease(listen, dev, force)
	if err != nil {
		return fail(err.Error())
	}
	defer releaseLease(listen, dev, l.ID)
	idPath := "/devices/" + url.PathEscape(dev)
	// created 也 stop 会把它变成 stopped，没必要；只停真在跑的。
	if st := l.Device.InstanceState; st != "created" && st != "stopped" {
		if err := httpPost(listen, idPath+"/stop", nil, nil); err != nil {
			return fail(err.Error())
		}
	}
	if reset {
		if err := httpPost(listen, idPath+"/config/reset", nil, nil); err != nil {
			return fail(err.Error())
		}
	}
	var row deviceRow
	if err := httpGet(listen, idPath, &row); err != nil {
		return fail(err.Error())
	}
	return out(map[string]any{"device_id": dev, "state": row.InstanceState, "overridden": row.Overridden})
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
	// source（live/tomb/disk）只在列表端点上，单条 /turns/{id} 不返。
	// 所以取列表再挑这一轮：HTTP 调用次数不变，source 才有值。
	var tlist struct {
		Turns  []json.RawMessage `json:"turns"`
		Source string            `json:"source"`
	}
	if err := httpGet(listen, "/devices/"+esc+"/turns"+q, &tlist); err != nil {
		return fail(err.Error())
	}
	var meta json.RawMessage
	for _, t := range tlist.Turns {
		var row struct {
			TurnID string `json:"turn_id"`
		}
		if json.Unmarshal(t, &row) == nil && row.TurnID == turnID {
			meta = t
			break
		}
	}
	if meta == nil {
		return fail("turn 未命中：" + turnID)
	}
	var evWrap struct {
		Events []map[string]any `json:"events"`
	}
	if err := httpGet(listen, "/devices/"+esc+"/events"+q, &evWrap); err != nil {
		return fail(err.Error())
	}
	filtered := compactEvents(evWrap.Events, turnID)
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
		"source":      tlist.Source,
		"turn":        json.RawMessage(meta),
		"events":      filtered,
		"frames":      st,
	})
}

// compactEvents 只留本轮事件，去掉顶层已有的 device_id / instance_id / turn_id，
// 连续的 tts_chunk 合成一条：count 是包数，payload_len 是字节和。一轮几十包分片
// 原样倒出来全是噪音。
func compactEvents(all []map[string]any, turnID string) []map[string]any {
	out := make([]map[string]any, 0, len(all))
	for _, e := range all {
		if s, _ := e["turn_id"].(string); s != turnID {
			continue
		}
		n, _ := e["payload_len"].(float64)
		if e["event_type"] == "tts_chunk" && len(out) > 0 && out[len(out)-1]["event_type"] == "tts_chunk" {
			last := out[len(out)-1]
			last["count"] = last["count"].(int) + 1
			last["payload_len"] = last["payload_len"].(float64) + n
			continue
		}
		c := make(map[string]any, len(e))
		for k, v := range e {
			if k != "device_id" && k != "instance_id" && k != "turn_id" {
				c[k] = v
			}
		}
		if e["event_type"] == "tts_chunk" {
			c["count"], c["payload_len"] = 1, n
		}
		out = append(out, c)
	}
	return out
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
	if callerWD != "" && !filepath.IsAbs(outFile) {
		outFile = filepath.Join(callerWD, outFile)
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
	return printGet(listen, "/devices/"+url.PathEscape(dev)+"/instances")
}
