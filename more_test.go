// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

// The typed helpers against the simulator, with authentication on.
func TestTypedHelpersAgainstSim(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plus2pm, "")
	st, err := shelly.GetStatus(ctx(t), c)
	if err != nil {
		t.Fatal(err)
	}
	sw, ok := st.Switch(0)
	if !ok || !sw.Output || sw.APower == nil || *sw.APower != 820.5 || sw.AEnergy.Total != 15234.5 {
		t.Fatalf("switch:0 %+v", sw)
	}
	cfg, err := shelly.GetConfig(ctx(t), c)
	if err != nil || cfg.Name() != "Kitchen" {
		t.Fatal(cfg.Name(), err)
	}
	swc, ok := cfg.Switch(0)
	if !ok || swc.InMode != "follow" || swc.InitialState != "match_input" || swc.PowerLimit == nil {
		t.Fatalf("switch config %+v", swc)
	}
	if w, ok := cfg.WiFi(); !ok || w.AP.SSID == nil || !strings.HasPrefix(*w.AP.SSID, "Plus2PM-") {
		t.Fatalf("wifi config %+v", w)
	}
	restart, err := shelly.SetConfig(ctx(t), c, "switch:0", map[string]any{"name": "Pump", "auto_off": true, "auto_off_delay": 30})
	if err != nil || restart {
		t.Fatal(restart, err)
	}
	cfg, _ = shelly.GetConfig(ctx(t), c)
	if swc, _ := cfg.Switch(0); swc.Name == nil || *swc.Name != "Pump" || !swc.AutoOff || swc.AutoOffDelay != 30 {
		t.Fatalf("after SetConfig %+v", swc)
	}
	if restart, err := shelly.SetConfig(ctx(t), c, "mqtt", map[string]any{"enable": true, "server": "192.0.2.5:1883"}); err != nil || !restart {
		t.Fatal("mqtt config", restart, err)
	}
	if _, err := shelly.SetConfig(ctx(t), c, "sys", map[string]any{"device": map[string]any{"name": "Kitchen 2"}}); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.Shelly(ctx(t)); info.Name != "Kitchen 2" {
		t.Fatalf("name after Sys.SetConfig: %q", info.Name)
	}
	raw, err := c.RPC(ctx(t), "Switch.GetConfig", map[string]any{"id": 9})
	if err == nil || !shelly.IsRPCError(err, -105) {
		t.Fatalf("missing component: %s %v", raw, err)
	}
	if was, err := shelly.SetSwitch(ctx(t), c, 1, true, 0); err != nil || was {
		t.Fatal(was, err)
	}
	if got, err := shelly.GetSwitch(ctx(t), c, 1); err != nil || !got.Output {
		t.Fatal(got, err)
	}
}

func TestScriptsAgainstSim(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plugS, "")
	id, err := shelly.CreateScript(ctx(t), c, "blink")
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	code := strings.Repeat("Shelly.call('Switch.Toggle', {id: 0}); // ünïcödé\n", 120) // over 5 kB, several pages
	if err := shelly.PutScriptCode(ctx(t), c, id, code, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ScriptCode(plugS, id); got != code {
		t.Fatal("the stored code differs")
	}
	back, err := shelly.GetScriptCode(ctx(t), c, id)
	if err != nil || back != code {
		t.Fatalf("read back %d octets, %v", len(back), err)
	}
	if _, err := shelly.EvalScript(ctx(t), c, id, "1+1"); err == nil {
		t.Fatal("eval in a stopped script accepted")
	}
	if was, err := shelly.StartScript(ctx(t), c, id); err != nil || was {
		t.Fatal(was, err)
	}
	if r, err := shelly.EvalScript(ctx(t), c, id, "40 + 2"); err != nil || r != "42" {
		t.Fatal(r, err)
	}
	list, err := shelly.ListScripts(ctx(t), c)
	if err != nil || len(list) != 1 || !list[0].Running || list[0].Name != "blink" {
		t.Fatal(list, err)
	}
	if st, err := shelly.GetScriptStatus(ctx(t), c, id); err != nil || !st.Running {
		t.Fatal(st, err)
	}
	if err := shelly.DeleteScript(ctx(t), c, id); err == nil {
		t.Fatal("a running script was deleted")
	}
	en, name := true, "blink2"
	if err := shelly.SetScriptConfig(ctx(t), c, id, &name, &en); err != nil {
		t.Fatal(err)
	}
	_, _ = shelly.StopScript(ctx(t), c, id)
	if err := shelly.DeleteScript(ctx(t), c, id); err != nil {
		t.Fatal(err)
	}
	if list, _ := shelly.ListScripts(ctx(t), c); len(list) != 0 {
		t.Fatal(list)
	}
}

func TestSchedulesAgainstSim(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plugS, "")
	job := shelly.ScheduleJob{Enable: true, Timespec: "0 30 6 * * MON-FRI", Calls: []shelly.ScheduleCall{{Method: "Switch.Set", Params: map[string]any{"id": 0, "on": false}}}}
	id, rev, err := shelly.CreateSchedule(ctx(t), c, job)
	if err != nil || id != 1 || rev != 1 {
		t.Fatal(id, rev, err)
	}
	if _, _, err := shelly.CreateSchedule(ctx(t), c, shelly.ScheduleJob{Timespec: "every morning", Calls: job.Calls}); !shelly.IsRPCError(err, -103) {
		t.Fatalf("a bad timespec: %v", err)
	}
	job.ID, job.Timespec = id, "@sunset-30m * * SUN"
	if rev, err := shelly.UpdateSchedule(ctx(t), c, job); err != nil || rev != 2 {
		t.Fatal(rev, err)
	}
	jobs, _, err := shelly.ListSchedules(ctx(t), c)
	if err != nil || len(jobs) != 1 || jobs[0].Timespec != "@sunset-30m * * SUN" {
		t.Fatal(jobs, err)
	}
	if failed := s.RunSchedule(plugS, id); failed != 0 {
		t.Fatalf("%d calls failed", failed)
	}
	if s.Component(plugS, "switch:0")["output"] != false {
		t.Fatal("the job did not switch off the plug")
	}
	st, _ := shelly.GetStatus(ctx(t), c)
	if sys, _ := st.Sys(); sys.ScheduleRev != 2 {
		t.Fatalf("schedule_rev %d", sys.ScheduleRev)
	}
	if rev, err := shelly.DeleteSchedule(ctx(t), c, id); err != nil || rev != 3 {
		t.Fatal(rev, err)
	}
}

func TestWebhooksFireAgainstSim(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hit := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path)
		mu.Unlock()
		hit <- struct{}{}
	}))
	defer srv.Close()
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plus2pm, "")
	ev, err := shelly.WebhookEvents(ctx(t), c)
	if err != nil || !contains(ev, "switch.on") || !contains(ev, "input.button_push") {
		t.Fatal(ev, err)
	}
	if _, _, err := shelly.CreateWebhook(ctx(t), c, shelly.Webhook{Event: "light.on", Enable: true, URLs: []string{srv.URL}}); err == nil {
		t.Fatal("an event the device lacks was accepted")
	}
	if _, _, err := shelly.CreateWebhook(ctx(t), c, shelly.Webhook{CID: 1, Event: "switch.on", Enable: true, URLs: []string{srv.URL + "/on1"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := shelly.CreateWebhook(ctx(t), c, shelly.Webhook{CID: 0, Event: "input.button_push", Enable: true, URLs: []string{srv.URL + "/push"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := shelly.SetSwitch(ctx(t), c, 1, true, 0); err != nil {
		t.Fatal(err)
	}
	s.Event(plus2pm, "input:0", "single_push")
	for i := 0; i < 2; i++ {
		select {
		case <-hit:
		case <-time.After(3 * time.Second):
			t.Fatalf("webhooks called: %v", got)
		}
	}
	mu.Lock()
	if !contains(got, "/on1") || !contains(got, "/push") {
		t.Fatal(got)
	}
	mu.Unlock()
	hooks, rev, err := shelly.ListWebhooks(ctx(t), c)
	if err != nil || len(hooks) != 2 || rev != 2 {
		t.Fatal(hooks, rev, err)
	}
	hooks[0].Enable = false
	if _, err := shelly.UpdateWebhook(ctx(t), c, hooks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := shelly.DeleteAllWebhooks(ctx(t), c); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Hooks(plus2pm)); n != 2 {
		t.Fatalf("%d hook calls", n)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestKVSAndVirtualAgainstSim(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plugS, "")
	etag, _, err := shelly.KVSSet(ctx(t), c, "mode", map[string]any{"eco": true}, "")
	if err != nil || etag == "" {
		t.Fatal(etag, err)
	}
	if _, _, err := shelly.KVSSet(ctx(t), c, "mode", 1, "stale"); err == nil {
		t.Fatal("a write with a stale etag was accepted")
	}
	for i := 0; i < 14; i++ {
		if _, _, err := shelly.KVSSet(ctx(t), c, "k"+string(rune('a'+i)), i, ""); err != nil {
			t.Fatal(err)
		}
	}
	it, err := shelly.KVSGet(ctx(t), c, "mode")
	if err != nil || string(it.Value) != `{"eco":true}` || it.Etag != etag {
		t.Fatal(it, err)
	}
	all, err := shelly.KVSGetMany(ctx(t), c, "k*")
	if err != nil || len(all) != 14 || all[13].Key != "kn" {
		t.Fatalf("%d items %v", len(all), err)
	}
	keys, _, err := shelly.KVSList(ctx(t), c, "")
	if err != nil || len(keys) != 15 || keys["mode"] != etag {
		t.Fatal(len(keys), err)
	}
	if _, err := shelly.KVSDelete(ctx(t), c, "mode", etag); err != nil {
		t.Fatal(err)
	}
	if _, err := shelly.KVSGet(ctx(t), c, "mode"); !shelly.IsRPCError(err, -105) {
		t.Fatal(err)
	}
	if len(s.KVS(plugS)) != 14 {
		t.Fatal(s.KVS(plugS))
	}

	id, err := shelly.AddVirtual(ctx(t), c, "number", map[string]any{"name": "Setpoint", "min": 10.0, "max": 30.0, "default_value": 21.0}, 0)
	if err != nil || id != 200 {
		t.Fatal(id, err)
	}
	if err := shelly.SetVirtual(ctx(t), c, "number:200", 35); !shelly.IsRPCError(err, -103) {
		t.Fatalf("out of range: %v", err)
	}
	if err := shelly.SetVirtual(ctx(t), c, "number:200", 23.5); err != nil {
		t.Fatal(err)
	}
	comps, _, err := shelly.GetComponents(ctx(t), c, true)
	if err != nil || len(comps) != 1 || comps[0].Key != "number:200" || !strings.Contains(string(comps[0].Status), "23.5") || !strings.Contains(string(comps[0].Config), "Setpoint") {
		t.Fatal(comps, err)
	}
	allComps, _, err := shelly.GetComponents(ctx(t), c, false)
	if err != nil || len(allComps) < 3 {
		t.Fatal(len(allComps), err)
	}
	if err := shelly.DeleteVirtual(ctx(t), c, "number:200"); err != nil {
		t.Fatal(err)
	}
	if comps, _, _ := shelly.GetComponents(ctx(t), c, true); len(comps) != 0 {
		t.Fatal(comps)
	}
}

// A cover with a travel time moves through opening to open, with its
// position reported on the way, and stops where Cover.Stop finds it.
func TestTimedCover(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const key = "shellyplus2pm-a8032ab1c001"
	s.Add(sim.Device{Gen: 2, ID: key, MAC: "A8032AB1C001", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4", CoverTravel: 2 * time.Second,
		Components: map[string]map[string]any{"cover:0": {"id": 0.0, "state": "stopped", "current_pos": 0.0, "apower": 0.0}, "sys": {"uptime": 1.0}}})
	c := client(s, key, "")
	if err := shelly.CoverToPosition(ctx(t), c, 0, 50); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	cv, _ := shelly.GetCover(ctx(t), c, 0)
	if cv.State != "opening" || cv.CurrentPos == nil || *cv.CurrentPos <= 0 || *cv.CurrentPos >= 50 || cv.TargetPos == nil || *cv.TargetPos != 50 {
		t.Fatalf("while moving %+v", cv)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cv, _ = shelly.GetCover(ctx(t), c, 0); cv.State == "stopped" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cv.State != "stopped" || *cv.CurrentPos != 50 {
		t.Fatalf("after the move %+v", cv)
	}
	if err := shelly.MoveCover(ctx(t), c, 0, "open", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := shelly.MoveCover(ctx(t), c, 0, "stop", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	stopped, _ := shelly.GetCover(ctx(t), c, 0)
	time.Sleep(500 * time.Millisecond)
	later, _ := shelly.GetCover(ctx(t), c, 0)
	if stopped.State != "stopped" || *stopped.CurrentPos <= 50 || *stopped.CurrentPos >= 100 || *later.CurrentPos != *stopped.CurrentPos {
		t.Fatalf("stop: %+v then %+v", stopped, later)
	}
}

func TestCountersAndInputTrigger(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, plus2pm, "")
	raw, err := c.RPC(ctx(t), "Switch.ResetCounters", map[string]any{"id": 0, "type": []any{"aenergy"}})
	if err != nil || !strings.Contains(string(raw), "15234.5") {
		t.Fatal(string(raw), err)
	}
	if sw, _ := shelly.GetSwitch(ctx(t), c, 0); sw.AEnergy.Total != 0 {
		t.Fatal(sw.AEnergy)
	}
	if _, err := c.RPC(ctx(t), "Input.Trigger", map[string]any{"id": 0, "event_type": "double_push"}); err != nil {
		t.Fatal(err)
	}
}

// A virtual component set over the WebSocket channel is answered: the
// change is notified after the answer, not while the asking channel
// holds the device's notification order (found by aioshelly).
func TestVirtualSetOverWebSocket(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c, _, err := websocket.Dial(ctx(t), "ws://"+s.Addr(plugS)+"/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for i, req := range []string{
		`{"id":1,"src":"t","method":"Virtual.Add","params":{"type":"boolean"}}`,
		`{"id":2,"src":"t","method":"Boolean.Set","params":{"id":200,"value":true}}`,
	} {
		if err := c.Write(ctx(t), websocket.MessageText, []byte(req)); err != nil {
			t.Fatal(err)
		}
		for {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, b, err := c.Read(rctx)
			cancel()
			if err != nil {
				t.Fatalf("request %d: no answer: %v", i+1, err)
			}
			if strings.Contains(string(b), `"id":`+strconv.Itoa(i+1)) {
				break
			}
		}
	}
	waitFor(t, "the value", func() bool { return s.Component(plugS, "boolean:200")["value"] == true })
}

// Gen1 settings beyond Wi-Fi and actions, with the shelly client.
func TestGen1SettingsAgainstSim(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	c := client(s, g1pm, "")
	m, err := c.Settings1(ctx(t), "relay", 0, url.Values{"default_state": {"last"}, "auto_off": {"30"}})
	if err != nil || m["default_state"] != "last" || m["auto_off"] != 30.0 {
		t.Fatal(m, err)
	}
	if _, err := c.Get1(ctx(t), shelly.Gen1SetAction("out_on_url", 0, true, "http://192.0.2.1/on")); err != nil {
		t.Fatal(err)
	}
	acts, err := c.Gen1Actions(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if a := acts["out_on_url"]; len(a) != 1 || !a[0].Enabled || a[0].URLs[0].URL != "http://192.0.2.1/on" {
		t.Fatalf("%+v", a)
	}
	if a := acts["out_off_url"]; len(a) != 1 || a[0].Enabled {
		t.Fatalf("%+v", a)
	}
}
