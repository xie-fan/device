package api

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"toy-device-simulator/protocol"
)

func TestStartOnlyCreatedOrStoppedElse409(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_st")
	code, body := e.post(t, "/devices/sim_st/start", nil)
	if code != http.StatusAccepted {
		t.Fatalf("Created 上 start 应 202，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_st/start", nil)
	if code != http.StatusConflict {
		t.Fatalf("非 Created/Stopped 再 start 应 409 且不 TryAcquire，得到 %d body=%s", code, body)
	}
}

func TestStartReturns202DoesNotWaitReady(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_nr")
	start := time.Now()
	code, body := e.post(t, "/devices/sim_nr/start", nil)
	elapsed := time.Since(start)
	if code != http.StatusAccepted {
		t.Fatalf("start 应 202，得到 %d body=%s", code, body)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("start 不得等待 Ready，耗时 %s", elapsed)
	}
	m := decodeMap(t, body)
	if strField(m, "instance_id") == "" || intField(m, "conn_generation") == 0 {
		t.Fatalf("202 应含 instance_id 与 conn_generation，body=%s", body)
	}
}

func TestWaitReadyMissingFields400(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_wr")
	code, body := e.post(t, "/devices/sim_wr/wait_ready", map[string]any{})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 instance_id/conn_generation 应 400，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_wr/wait_ready", map[string]any{"instance_id": "ins_x"})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 conn_generation 应 400，得到 %d body=%s", code, body)
	}
}

func TestWaitReadyMissLiveAndTombstone404(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_miss")
	code, body := e.post(t, "/devices/sim_miss/wait_ready", map[string]any{
		"instance_id": "ins_never", "conn_generation": 1, "timeout_sec": 1,
	})
	if code != http.StatusNotFound {
		t.Fatalf("live 与 tombstone 都未命中应 404，得到 %d body=%s", code, body)
	}
}

func TestWaitReadyTombstone409GenerationGone(t *testing.T) {
	e := newEnv(t)
	ins := e.createDevice(t, "sim_tb")
	code, body := e.del(t, "/devices/sim_tb")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_tb/wait_ready", map[string]any{
		"instance_id": ins, "conn_generation": 1, "timeout_sec": 1,
	})
	if code != http.StatusConflict || !containsBytes(body, "generation_gone") {
		t.Fatalf("命中 tombstone 应 409 generation_gone，得到 %d body=%s", code, body)
	}
}

func TestWaitReadyFinalizeCommitted409EvenIfWasSpeakable(t *testing.T) {
	e := newEnv(t)
	ins, gen := e.createStartReady(t, "sim_fc")
	code, body := e.post(t, "/devices/sim_fc/stop", nil)
	if code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_fc/wait_ready", map[string]any{
		"instance_id": ins, "conn_generation": gen, "timeout_sec": 1,
	})
	if code != http.StatusConflict || !containsBytes(body, "generation_gone") {
		t.Fatalf("finalize_committed 应 409 generation_gone（无论曾否 speakable），得到 %d body=%s", code, body)
	}
}

func TestWaitReadyWrongGeneration409(t *testing.T) {
	e := newEnv(t)
	ins, gen := e.createStartReady(t, "sim_wg")
	code, body := e.post(t, "/devices/sim_wg/wait_ready", map[string]any{
		"instance_id": ins, "conn_generation": gen + 9, "timeout_sec": 1,
	})
	if code != http.StatusConflict || !containsBytes(body, "generation_gone") {
		t.Fatalf("generation 不匹配应 409 generation_gone，得到 %d body=%s", code, body)
	}
}

func TestWaitReadySpeakableMatchingGeneration200(t *testing.T) {
	e := newEnv(t)
	ins, gen := e.createStartReady(t, "sim_ok")
	code, body := e.post(t, "/devices/sim_ok/wait_ready", map[string]any{
		"instance_id": ins, "conn_generation": gen, "timeout_sec": 1,
	})
	if code != http.StatusOK {
		t.Fatalf("已 speakable 且 generation 匹配应立即 200，得到 %d body=%s", code, body)
	}
	m := decodeMap(t, body)
	if strField(m, "instance_id") != ins || intField(m, "conn_generation") != gen {
		t.Fatalf("200 body 字段不对: %s", body)
	}
	if strField(m, "connection_state") == "" {
		t.Fatalf("200 应含 connection_state，body=%s", body)
	}
}

func TestWaitReadyTimeout504OnlyIfWaiterStillRegistered(t *testing.T) {
	e := newEnv(t)
	e.auto.silent = true
	e.createDevice(t, "sim_to")
	ins, gen := e.startDevice(t, "sim_to")
	code, body := e.post(t, "/devices/sim_to/wait_ready", map[string]any{
		"instance_id": ins, "conn_generation": gen, "timeout_sec": 1,
	})
	if code != http.StatusGatewayTimeout {
		t.Fatalf("waiter 仍在表中且未 committed 超时应 504，得到 %d body=%s", code, body)
	}

	e.auto.silent = false
	ins2, gen2 := e.createStartReady(t, "sim_to2")
	done := make(chan [2]any, 1)
	go func() {
		c, b := e.post(t, "/devices/sim_to2/wait_ready", map[string]any{
			"instance_id": ins2, "conn_generation": gen2, "timeout_sec": 5,
		})
		done <- [2]any{c, b}
	}()
	select {
	case got := <-done:
		c := got[0].(int)
		b := got[1].([]byte)
		if c != http.StatusOK {
			t.Fatalf("已 speakable 不得再 504，得到 %d body=%s", c, b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("已 speakable 的 wait_ready 应立即 200，不得空等到超时")
	}
}

func TestNewGenerationReadyDoesNotWakeOldWaiter(t *testing.T) {
	e := newEnv(t)
	e.createDevice(t, "sim_ng")
	ins, gen := e.startDevice(t, "sim_ng")
	e.waitReady(t, "sim_ng", ins, gen)
	code, body := e.post(t, "/devices/sim_ng/stop", nil)
	if code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d body=%s", code, body)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var oldCode int
	var oldBody []byte
	go func() {
		defer wg.Done()
		oldCode, oldBody = e.post(t, "/devices/sim_ng/wait_ready", map[string]any{
			"instance_id": ins, "conn_generation": gen, "timeout_sec": 5,
		})
	}()
	time.Sleep(50 * time.Millisecond)
	ins2, gen2 := e.startDevice(t, "sim_ng")
	e.waitReady(t, "sim_ng", ins2, gen2)
	wg.Wait()
	if oldCode == http.StatusOK {
		t.Fatalf("新一代 Ready 不得唤醒上一代 waiter，得到 200 body=%s", oldBody)
	}
	if oldCode != http.StatusConflict || !containsBytes(oldBody, "generation_gone") {
		t.Fatalf("上一代 waiter 应为 409 generation_gone，得到 %d body=%s", oldCode, oldBody)
	}
}

func TestStopDeleteUseBeginCloseNotCancelTurn(t *testing.T) {
	e := newEnv(t)
	e.auto.replyTTS = false
	ins, _ := e.createStartReady(t, "sim_bc")
	assetID := e.uploadWAV(t)
	code, body := e.post(t, "/devices/sim_bc/speak", map[string]any{"asset_id": assetID})
	if code != http.StatusAccepted {
		t.Fatalf("speak 应 202，得到 %d body=%s", code, body)
	}
	code, body = e.post(t, "/devices/sim_bc/stop", nil)
	if code != http.StatusOK {
		t.Fatalf("stop 应 200，得到 %d body=%s", code, body)
	}
	c := e.conn("sim_bc")
	if c == nil || c.CloseCount() == 0 {
		t.Fatal("stop 必须走 BeginClose（关连接），不得只用 CancelTurn 保持连接")
	}

	e.createStartReady(t, "sim_del")
	code, body = e.del(t, "/devices/sim_del")
	if code != http.StatusOK {
		t.Fatalf("DELETE 应 200，得到 %d body=%s", code, body)
	}
	if !containsBytes(body, `"deleted":true`) && !containsBytes(body, `"deleted": true`) {
		t.Fatalf("DELETE 200 应含 deleted=true，body=%s", body)
	}
	c2 := e.conn("sim_del")
	if c2 == nil || c2.CloseCount() == 0 {
		t.Fatal("delete 必须走 BeginClose，不得 CancelTurn")
	}
	_ = ins
	if c != nil && !c.HasStage(protocol.StageBreak) {
		// Reserved 可能不发 Stage=3；Speaking 才发。此处只禁止「不断连」。
	}
}
