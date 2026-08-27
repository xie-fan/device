package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Phase 4b 静默成功探针：timeout 静默终态后发探针 report，
// echo_ok=倾向静默成功，echo_timeout=倾向被 drop；默认关闭。

func probeBody(e *testEnv, id string, on bool) map[string]any {
	body := e.deviceBody(id)
	beh, _ := body["behavior"].(map[string]any)
	beh["first_reply_timeout_sec"] = 1
	beh["report_echo_timeout_sec"] = 1
	if on {
		beh["silence_probe"] = true
	}
	return body
}

// speakTimeout 触发一次静默 timeout 终态（fake 服务端不回任何 turn 下行）。
func speakTimeout(t *testing.T, e *testEnv, id, assetID string) {
	t.Helper()
	code, raw := e.post(t, "/devices/"+id+"/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d %s", code, raw)
	}
	m := decodeMap(t, raw)
	if strField(m, "turn_end_reason") != "timeout" {
		t.Fatalf("应以 timeout 终态: %s", raw)
	}
}

// waitEvent 轮询事件带直到出现同时含 needle 各子串的行。
func waitEvent(t *testing.T, e *testEnv, id, ins string, timeout time.Duration, needles ...string) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []byte
	for time.Now().Before(deadline) {
		code, raw, _ := e.get(t, "/devices/"+id+"/events?instance_id="+ins)
		if code == http.StatusOK {
			last = raw
			all := true
			for _, n := range needles {
				if !containsBytes(raw, n) {
					all = false
					break
				}
			}
			if all {
				return raw
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待事件 %s 超时，最后事件带: %s", strings.Join(needles, "+"), last)
	return nil
}

func TestSilenceProbeEchoOK(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", e.createBody(probeBody(e, "sim_prb", true)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_prb")
	e.waitReady(t, "sim_prb", ins, gen)
	assetID := e.uploadWAV(t)

	speakTimeout(t, e, "sim_prb", assetID)
	// fake 服务端持续回显 report → 探针应得 echo_ok。
	waitEvent(t, e, "sim_prb", ins, 3*time.Second, "silence_probe", "echo_ok")
}

func TestSilenceProbeEchoTimeout(t *testing.T) {
	e := newEnv(t)
	// 只回显初始 report（Ready 需要），之后停回 → 探针 echo 超时。
	e.auto.echoLimit = 1
	code, raw := e.post(t, "/devices", e.createBody(probeBody(e, "sim_prbt", true)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_prbt")
	e.waitReady(t, "sim_prbt", ins, gen)
	assetID := e.uploadWAV(t)

	speakTimeout(t, e, "sim_prbt", assetID)
	waitEvent(t, e, "sim_prbt", ins, 4*time.Second, "silence_probe", "echo_timeout")
}

func TestSilenceProbeOffByDefault(t *testing.T) {
	e := newEnv(t)
	code, raw := e.post(t, "/devices", e.createBody(probeBody(e, "sim_prb0", false)))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_prb0")
	e.waitReady(t, "sim_prb0", ins, gen)
	assetID := e.uploadWAV(t)

	speakTimeout(t, e, "sim_prb0", assetID)
	time.Sleep(300 * time.Millisecond)
	_, evRaw, _ := e.get(t, "/devices/sim_prb0/events?instance_id="+ins)
	if containsBytes(evRaw, "silence_probe") {
		t.Fatalf("默认关闭不应有 silence_probe 事件: %s", evRaw)
	}
}

func TestSilenceProbeSkippedWhenReplied(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = true
	body := probeBody(e, "sim_prbr", true)
	beh, _ := body["behavior"].(map[string]any)
	beh["downlink_idle_timeout_sec"] = 1
	code, raw := e.post(t, "/devices", e.createBody(body))
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, raw)
	}
	ins, gen := e.startDevice(t, "sim_prbr")
	e.waitReady(t, "sim_prbr", ins, gen)
	assetID := e.uploadWAV(t)

	// 有 TTS 回复：即便后续 idle timeout，也非「全程静默」，不发探针。
	code, raw = e.post(t, "/devices/sim_prbr/speak_and_wait", map[string]any{
		"asset_id": assetID, "timeout_sec": 8,
	})
	if code != http.StatusOK {
		t.Fatalf("speak_and_wait 应 200，得到 %d %s", code, raw)
	}
	time.Sleep(300 * time.Millisecond)
	_, evRaw, _ := e.get(t, "/devices/sim_prbr/events?instance_id="+ins)
	if containsBytes(evRaw, "silence_probe") {
		t.Fatalf("有下行包的 turn 不应触发探针: %s", evRaw)
	}
}
