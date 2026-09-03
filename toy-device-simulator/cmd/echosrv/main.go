// echosrv 是本地验收用的极简协议服务端：注册即 ack、report 原样回显、
// 收到上行首帧后回一段可听见的 440Hz TTS（慢推，模拟真实下行节奏）。
// 下行格式跟随该轮上行格式，分包形态照抄真实服务端（见 downlink.go）；
// 无 ffmpeg 或转不出目标格式时退回 PCM。
// 不发结束标志——设备靠 downlink_idle_timeout_sec 自行终态，这与真实服务端
// 静默收尾的行为一致。仅供 UI/联调，无任何生产语义。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"toy-device-simulator/media"
	"toy-device-simulator/protocol"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func main() {
	fs := flag.NewFlagSet("echosrv", flag.ExitOnError)
	addr := fs.String("listen", "127.0.0.1:8089", "WS 监听地址")
	ttsMs := fs.Int("tts-ms", 2000, "回放 TTS 时长（毫秒）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	tc, terr := media.Detect("")
	if terr != nil {
		fmt.Fprintf(os.Stderr, "echosrv: 未找到 ffmpeg，下行一律回 pcm：%v\n", terr)
	} else {
		fmt.Fprintf(os.Stderr, "echosrv: ffmpeg 编码能力 %s\n", tc.Capabilities())
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		log.Printf("连接 %s path=%s", r.RemoteAddr, r.URL.Path)
		go serve(c, tc, *ttsMs)
	})
	fmt.Fprintf(os.Stderr, "echosrv listening on %s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func serve(c *websocket.Conn, tc *media.Toolchain, ttsMs int) {
	defer c.Close()
	var wmu sync.Mutex
	send := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return c.WriteMessage(websocket.BinaryMessage, b)
	}
	var lastUUID uint32
	replied := map[uint32]bool{}
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case protocol.FirstManage:
			env, err := protocol.DecodeManage(msg)
			if err != nil {
				continue
			}
			if strings.HasSuffix(env.Topic, "/register/server") {
				ack, _ := protocol.EncodeManage(strings.TrimSuffix(env.Topic, "server")+"client", protocol.RegisterAck{Code: 0})
				_ = send(ack)
			}
			if strings.HasSuffix(env.Topic, "/report/server") {
				echo, _ := protocol.EncodeManage(strings.TrimSuffix(env.Topic, "server")+"client", json.RawMessage(env.Data))
				_ = send(echo)
			}
		case protocol.FirstAudio:
			view := protocol.Inspect(msg)
			if !view.OKHeader || view.Header.Stage != protocol.StageUploading {
				continue
			}
			uuid := view.Header.UUID
			if uuid != lastUUID {
				lastUUID = uuid
				// 新 turn；旧 uuid 的记录不再有用
				for k := range replied {
					delete(replied, k)
				}
			}
			if replied[uuid] {
				continue
			}
			replied[uuid] = true
			format := strings.TrimRight(string(view.Header.AudioFormat[:]), "\x00")
			go streamTTS(send, tc, uuid, format, int(view.Header.SamplingRate), ttsMs)
		}
	}
}

// streamTTS 慢推一段正弦音，贴近真实服务端的下行节奏，
// 给「TTS 播放期间再送一条 → 排队」的验收留出操作窗口。
// 格式与分包见 downlink.go；节奏按各包字节占比分摊 totalMs——
// 20 KB 的 AAC 包本就对应一秒多音频，固定 100ms 一帧会失真。
func streamTTS(send func([]byte) error, tc *media.Toolchain, uuid uint32, upFormat string, upRate int, totalMs int) {
	rate := upRate
	if rate <= 0 {
		rate = 16000
	}
	if upFormat == "" {
		upFormat = media.FormatPCM
	}
	packets, format := buildDownlink(tc, upFormat, rate, totalMs)
	if len(packets) == 0 {
		return
	}
	total := 0
	for _, p := range packets {
		total += len(p)
	}
	for seq, p := range packets {
		h := protocol.NewAudioHeader(format, protocol.StageUploading, uint32(seq), uuid, 0, uint32(rate))
		frame, err := protocol.EncodeAudioFrame(h, p)
		if err != nil {
			return
		}
		if send(frame) != nil {
			return
		}
		// 包间隔按字节占比分摊，但封顶 1s：20 KB 的包对应一秒多音频，
		// 照字节占比等出来的间隔会超过设备的 downlink_idle_timeout_sec，
		// turn 在第一包后就 idle 收尾、后面的包全丢（实测过）。
		gap := time.Duration(totalMs) * time.Millisecond * time.Duration(len(p)) / time.Duration(total)
		if gap > time.Second {
			gap = time.Second
		}
		time.Sleep(gap)
	}
	log.Printf("TTS 完成 uuid=%d format=%s(上行 %s) rate=%d packets=%d bytes=%d",
		uuid, format, upFormat, rate, len(packets), total)
}
