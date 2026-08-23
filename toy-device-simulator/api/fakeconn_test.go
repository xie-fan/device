package api

import (
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
	c.mu.Unlock()
	select {
	case c.writeCh <- cp:
	default:
	}
	// 仅堵住音频写出，避免握手（register/report）被 hold 卡死。
	if c.writeGate != nil && len(data) > 0 && data[0] == protocol.FirstAudio {
		select {
		case <-c.writeGate:
		case <-c.closed:
			return net.ErrClosed
		}
	}
	return nil
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
						ack, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", protocol.RegisterAck{Code: 0})
						c.Push(ack)
					}
					if len(env.Topic) >= len("/report/server") && env.Topic[len(env.Topic)-len("/report/server"):] == "/report/server" {
						echo, _ := protocol.EncodeManage(env.Topic[:len(env.Topic)-len("server")]+"client", json.RawMessage(env.Data))
						c.Push(echo)
					}
				case protocol.FirstAudio:
					view := protocol.Inspect(msg)
					if !view.OKHeader || view.Header.Stage != protocol.StageUploading {
						continue
					}
					if opts.failJSON && !sentFail {
						sentFail = true
						c.Push([]byte(`{"RequestID":"r1","Code":1,"CodeMsg":"音频处理失败，请稍后重试","Data":null}`))
						continue
					}
					if opts.replyTTS && !sentTTS {
						sentTTS = true
						h := protocol.NewPCMHeader(protocol.StageUploading, 0, view.Header.UUID, 4, 16000)
						if opts.needAck {
							h.NeedAck = 1
						}
						frame, _ := protocol.EncodeAudioFrame(h, []byte{0x01, 0x02, 0x03, 0x04})
						c.Push(frame)
					}
				}
			case <-c.closed:
				return
			}
		}
	}()
}
