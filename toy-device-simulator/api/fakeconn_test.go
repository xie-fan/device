package api

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"sync"
	"time"

	"toy-device-simulator/core"
	"toy-device-simulator/protocol"
)

type fakeConn struct {
	mu         sync.Mutex
	writes     [][]byte
	writeCh    chan []byte
	inbound    chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
	closeCount int
	writeGate  chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		writeCh: make(chan []byte, 1024),
		inbound: make(chan []byte, 64),
		closed:  make(chan struct{}),
	}
}

var _ core.Conn = (*fakeConn)(nil)

func (c *fakeConn) WriteMessage(_ int, data []byte) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	cp := append([]byte(nil), data...)
	c.mu.Lock()
	c.writes = append(c.writes, cp)
	gate := c.writeGate
	c.mu.Unlock()
	select {
	case c.writeCh <- cp:
	default:
	}
	// 仅堵住音频写出，避免握手（register/report）被 hold 卡死。
	if gate != nil && len(data) > 0 && data[0] == protocol.FirstAudio {
		select {
		case <-gate:
		case <-c.closed:
			return net.ErrClosed
		}
	}
	return nil
}

func (c *fakeConn) ReleaseWriteGate() {
	c.mu.Lock()
	g := c.writeGate
	c.writeGate = nil
	c.mu.Unlock()
	if g != nil {
		close(g)
	}
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	select {
	case m := <-c.inbound:
		return 1, m, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func (c *fakeConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeCount++
		c.mu.Unlock()
		close(c.closed)
	})
	return nil
}

func (c *fakeConn) Push(msg []byte) { c.inbound <- append([]byte(nil), msg...) }

func (c *fakeConn) Writes() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.writes))
	copy(out, c.writes)
	return out
}

func (c *fakeConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

func (c *fakeConn) HasStage(stage uint32) bool {
	for _, w := range c.Writes() {
		if len(w) <= protocol.HeaderBytes || w[0] != protocol.FirstAudio {
			continue
		}
		h, err := protocol.DecodeHeader(w[1:])
		if err == nil && h.Stage == stage {
			return true
		}
	}
	return false
}

func startAuto(c *fakeConn, opts autoOpts) {
	if opts.silent {
		return
	}
	go func() {
		sentTTS := false
		sentFail := false
		sentPhoto, sentPhotoReply := false, false
		topicPrefix := "" // 例如 demo/A3/sim_x/，从 register 的 topic 里取
		echoed := 0
		for {
			select {
			case msg := <-c.writeCh:
				if len(msg) == 0 {
					continue
				}
				switch msg[0] {
				case protocol.FirstManage:
					env, err := protocol.DecodeManage(msg)
					if err != nil {
						continue
					}
					if len(env.Topic) >= len("/register/server") && env.Topic[len(env.Topic)-len("/register/server"):] == "/register/server" {
						topicPrefix = env.Topic[:len(env.Topic)-len("register/server")]
						ack, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", protocol.RegisterAck{Code: 0})
						c.Push(ack)
					}
					if len(env.Topic) >= len("/report/server") && env.Topic[len(env.Topic)-len("/report/server"):] == "/report/server" {
						if opts.echoLimit <= 0 || echoed < opts.echoLimit {
							echoed++
							echo, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", json.RawMessage(env.Data))
							c.Push(echo)
						}
					}
				case protocol.FirstAudio:
					view := protocol.Inspect(msg)
					if !view.OKHeader || view.Header.Stage != protocol.StageUploading {
						continue
					}
					if opts.photoCmd && !sentPhoto && topicPrefix != "" {
						sentPhoto = true
						c.Push(photoCommandFrame(topicPrefix, opts))
					}
					if opts.failJSON && !sentFail {
						sentFail = true
						c.Push([]byte(`{"RequestID":"r1","Code":1,"CodeMsg":"音频处理失败，请稍后重试","Data":null}`))
						continue
					}
					if opts.replyTTS && !sentTTS {
						sentTTS = true
						format := opts.ttsFormat
						if format == "" {
							format = "pcm"
						}
						sr := opts.ttsSampleRate
						if sr == 0 {
							sr = 16000
						}
						payload := opts.ttsPayload
						if payload == nil {
							payload = []byte{0x01, 0x02, 0x03, 0x04}
						}
						h := protocol.NewAudioHeader(format, protocol.StageUploading, 0, view.Header.UUID, uint32(len(payload)), uint32(sr))
						if opts.needAck {
							h.NeedAck = 1
						}
						frame, _ := protocol.EncodeAudioFrame(h, payload)
						c.Push(frame)
					}
				case protocol.FirstImage:
					if !opts.photoReply || sentPhotoReply || len(msg) < 1+protocol.ImageHeaderBytes {
						continue
					}
					h, err := protocol.DecodeImageHeader(msg[1:])
					if err != nil || h.Stage != protocol.ImageStageFinished {
						continue
					}
					sentPhotoReply = true
					go pushPhotoReply(c, opts)
				}
			case <-c.closed:
				return
			}
		}
	}()
}

// photoCommandFrame 照真实服务端 service/skills/camera.go 的形状拼一条拍照指令。
func photoCommandFrame(topicPrefix string, opts autoOpts) []byte {
	key := opts.photoKey
	if key == "" {
		key = "0123456789abcdef"
	}
	movement := map[string]any{"behavior": 601, "start_text": "好的，我拍一下"}
	if len(opts.photoStartVoice) > 0 {
		movement["start_voice"] = base64.StdEncoding.EncodeToString(opts.photoStartVoice)
	}
	cmd, _ := protocol.EncodeManage(topicPrefix+"command/client", map[string]any{
		"code": 0, "message": "", "sequence_number": 0, "total": 1,
		"movement": movement,
		"data":     map[string]any{"Num": 1, "QuestionKey": key},
	})
	return cmd
}

// pushPhotoReply 模拟服务端图片分析后的语音回复：UUID 恒为 0（module/imageModule/analysis.go）。
func pushPhotoReply(c *fakeConn, opts autoOpts) {
	time.Sleep(opts.photoReplyDelay)
	payload := opts.photoReplyPayload
	if payload == nil {
		payload = []byte{9, 9, 9, 9}
	}
	h := protocol.NewAudioHeader("pcm", protocol.StageUploading, 0, 0, uint32(len(payload)), 16000)
	frame, _ := protocol.EncodeAudioFrame(h, payload)
	select {
	case <-c.closed:
	default:
		c.Push(frame)
	}
}
