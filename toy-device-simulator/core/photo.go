package core

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"toy-device-simulator/protocol"
)

// Photo 带图送话随本轮上行的那张图（phase14 §3）。字节在受理时读好，
// 之后改、删资产都不影响这一轮。
type Photo struct {
	AssetID string // 只进事件 reason
	Format  string // jpg|png|bmp
	Data    []byte
}

// uploadTurnPhoto 发音频之前先用本轮 UUID 传图，QuestionKey 与 Reserved 全 0：
// 服务端只存图，等同 UUID 的语音轮来取。返回 false = 本轮已不在（打断/收口），
// 调用方不再发音频；冻结只停传图，音频循环看到同一状态会自己收手。
func (d *DeviceInstance) uploadTurnPhoto(turnID string, uuid uint32) bool {
	d.deviceMu.Lock()
	tr := d.turn
	if tr == nil || tr.id != turnID || tr.photo == nil {
		d.deviceMu.Unlock()
		return true
	}
	p := *tr.photo
	interval := time.Duration(cmp.Or(d.cfg.Features.Photo.SliceIntervalMs, 50)) * time.Millisecond
	d.deviceMu.Unlock()

	frames, err := protocol.BuildImageFrames(p.Data, p.Format, "", "", uuid)
	if err != nil {
		d.notePhotoSkipped(turnID, "组帧失败")
		return true
	}
	for i, raw := range frames {
		if i > 0 {
			select {
			case <-time.After(interval):
			case <-d.finalizeDone:
				return false
			}
		}
		d.deviceMu.Lock()
		if d.finalizeStarted || d.slot.ID() != turnID || d.slot.State() == TurnTerminal {
			d.deviceMu.Unlock()
			return false
		}
		if tr.frozen {
			d.deviceMu.Unlock()
			return true
		}
		// 持 deviceMu 入队，与音频帧同理：CancelTurn 的 Stage=3 不会插到图片分片前面。
		err := d.enqueueData(Frame{Kind: KindManage, TurnID: turnID, Raw: raw, turn: tr})
		d.deviceMu.Unlock()
		if err != nil {
			if errors.Is(err, ErrDataFull) {
				d.requestFinalizeAsync("write_backpressure", false)
			}
			return false
		}
	}
	d.savePhoto(tr, uuid, p.Format, p.Data)
	d.deviceMu.Lock()
	_, n := d.appendEventLocked("photo_uploaded", turnID, photoReason("speak", p.AssetID, uuid, len(p.Data), len(frames)), "", "", "")
	d.deviceMu.Unlock()
	d.finishCritical([]EventNotify{n}, TerminalNotify{})
	return true
}

// photoReason photo_uploaded 的 reason（phase14 §5），两条路径同形。
func photoReason(source, assetID string, uuid uint32, bytes, slices int) string {
	return fmt.Sprintf("source=%s asset=%s uuid=%d bytes=%d slices=%d", source, assetID, uuid, bytes, slices)
}

// savePhoto 传出的图留档：本轮目录 photo_<uuid>.<格式>，一次上传一个文件，
// 同一轮两次上传互不覆盖。走 uplink 开关：save_uplink_audio=false 时不存。
func (d *DeviceInstance) savePhoto(tr *turnRuntime, uuid uint32, format string, data []byte) {
	if tr == nil {
		return
	}
	d.recorder.SubmitPCM(filepath.Join(filepath.Dir(tr.upPath), photoName(uuid, format)), data, true)
}

func photoName(uuid uint32, format string) string {
	return fmt.Sprintf("photo_%d.%s", uuid, format)
}

// FindPhoto 在本轮目录里找 uuid 那次传图的留档，format 取扩展名。
func FindPhoto(turnDir string, uuid uint32) (path, format string, ok bool) {
	for _, f := range []string{"jpg", "png", "bmp"} {
		p := filepath.Join(turnDir, photoName(uuid, f))
		if _, err := os.Stat(p); err == nil {
			return p, f, true
		}
	}
	return "", "", false
}
