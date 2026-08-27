// echosrv 是本地验收用的极简协议服务端：注册即 ack、report 原样回显、
// 收到上行首帧后回一段可听见的 440Hz TTS（PCM 分帧慢推，模拟真实下行节奏）。
// 不发结束标志——设备靠 downlink_idle_timeout_sec 自行终态，这与真实服务端
// 静默收尾的行为一致。仅供 UI/联调，无任何生产语义。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

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
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		log.Printf("连接 %s path=%s", r.RemoteAddr, r.URL.Path)
		go serve(c, *ttsMs)
	})
	fmt.Fprintf(os.Stderr, "echosrv listening on %s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func serve(c *websocket.Conn, ttsMs int) {
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
			go streamTTS(send, uuid, ttsMs)
		}
	}
}

// streamTTS 按 100ms 一帧慢推正弦音，贴近真实服务端的下行节奏，
// 给「TTS 播放期间再送一条 → 排队」的验收留出操作窗口。
func streamTTS(send func([]byte) error, uuid uint32, totalMs int) {
	const rate = 16000
	frames := totalMs / 100
	if frames < 1 {
		frames = 1
	}
	samplesPerFrame := rate / 10
	seq := uint32(0)
	for f := 0; f < frames; f++ {
		payload := make([]byte, samplesPerFrame*2)
		for i := 0; i < samplesPerFrame; i++ {
			t := float64(f*samplesPerFrame+i) / rate
			v := int16(8000 * math.Sin(2*math.Pi*440*t))
			payload[2*i] = byte(v)
			payload[2*i+1] = byte(v >> 8)
		}
		h := protocol.NewPCMHeader(protocol.StageUploading, seq, uuid, 0, rate)
		frame, err := protocol.EncodeAudioFrame(h, payload)
		if err != nil {
			return
		}
		if send(frame) != nil {
			return
		}
		seq++
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("TTS 完成 uuid=%d frames=%d", uuid, frames)
}
