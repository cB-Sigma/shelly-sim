// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
	"github.com/cB-Sigma/shelly/bthome"
)

// hookSink is a HookHandler on a test server that keeps what it got.
type hookSink struct {
	mu    sync.Mutex
	hooks []shelly.Hook
	srv   *httptest.Server
}

func newHookSink(t *testing.T, token string) *hookSink {
	k := &hookSink{}
	h := &shelly.HookHandler{Prefix: "/hooks", Token: token, Handle: func(x shelly.Hook) {
		k.mu.Lock()
		k.hooks = append(k.hooks, x)
		k.mu.Unlock()
	}}
	mux := http.NewServeMux()
	mux.Handle("/hooks/", h)
	mux.Handle("/hooks", h)
	k.srv = httptest.NewServer(mux)
	t.Cleanup(k.srv.Close)
	return k
}

func (k *hookSink) base() string { return k.srv.URL + "/hooks" }

func (k *hookSink) wait(t *testing.T, n int) []shelly.Hook {
	t.Helper()
	waitFor(t, "hooks", func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return len(k.hooks) >= n
	})
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]shelly.Hook(nil), k.hooks...)
}

// Gen2+ webhooks with URL tokens, received by HookHandler.
func TestHookHandlerWithGen2Webhooks(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const ht = "shellyplusht-a8032ab1e301"
	s.Add(sim.Device{Gen: 2, ID: ht, MAC: "A8032AB1E301", Model: "SNSN-0013A", App: "PlusHT", FW: "1.4.4",
		Components: map[string]map[string]any{"temperature:0": {"id": 0.0, "tC": 20.5, "tF": 68.9}, "switch:0": {"id": 0.0, "output": false, "apower": 12.5},
			"sys": {"uptime": 1.0}}})
	k := newHookSink(t, "s3cret")
	c := client(s, ht, "")
	events, err := shelly.WebhookEvents(ctx(t), c)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		found = found || e == "temperature.change"
	}
	if !found {
		t.Fatalf("events %v", events)
	}
	for _, w := range []shelly.Webhook{
		shelly.HookWebhook(k.base(), ht, "temperature.change", 0, map[string]string{"tC": "${ev.tC}", "mac": "${config.sys.device.mac}"}, "s3cret"),
		shelly.HookWebhook(k.base(), ht, "switch.on", 0, map[string]string{"apower": `${status["switch:0"].apower}`}, "s3cret"),
	} {
		if _, _, err := shelly.CreateWebhook(ctx(t), c, w); err != nil {
			t.Fatal(err)
		}
	}
	s.SetComponent(ht, "temperature:0", map[string]any{"tC": 22.25, "tF": 72.05})
	if _, err := shelly.SetSwitch(ctx(t), c, 0, true, 0); err != nil {
		t.Fatal(err)
	}
	got := k.wait(t, 2)
	byEvent := map[string]shelly.Hook{}
	for _, h := range got {
		byEvent[h.Event] = h
	}
	tc := byEvent["temperature.change"]
	if v, ok := tc.Float("tC"); !ok || v != 22.25 || tc.Device != ht || tc.Fields.Get("mac") != "A8032AB1E301" {
		t.Errorf("temperature hook %+v", tc)
	}
	if v, ok := byEvent["switch.on"].Float("apower"); !ok || v != 12.5 {
		t.Errorf("switch hook %+v", byEvent["switch.on"])
	}
}

// Gen1 action URLs and a sensor's report URL, received by HookHandler.
func TestHookHandlerWithGen1Actions(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	k := newHookSink(t, "")
	c := client(s, g1pm, "")
	on := shelly.HookURL(k.base(), "g1pm", "out_on", nil, "")
	if _, err := c.Get1(ctx(t), shelly.Gen1SetAction(shelly.Gen1OutputOn, 0, true, on)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get1(ctx(t), shelly.Gen1Turn("relay", 0, "off")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get1(ctx(t), shelly.Gen1Turn("relay", 0, "on")); err != nil {
		t.Fatal(err)
	}
	got := k.wait(t, 1)
	if got[0].Device != "g1pm" || got[0].Event != "out_on" {
		t.Errorf("action hook %+v", got[0])
	}

	temp, hum, bat := 24.5, 61.0, 2.9
	const htKey = "98CDAC1F3001"
	s.Add(sim.Device{Gen: 1, MAC: htKey, Model: "SHHT-1", FW: "20230913-114008/v1.14.0-gcb84623", TempC: &temp, Hum: &hum, Battery: &bat})
	ht := client(s, htKey, "")
	if _, err := ht.Get1(ctx(t), shelly.Gen1SetAction(shelly.Gen1SensorReport, 0, true, k.base())); err != nil {
		t.Fatal(err)
	}
	s.Report1(htKey)
	got = k.wait(t, 2)
	r := got[1]
	if v, ok := r.Float("hum"); !ok || v != 61 || r.Device != "shht-1" {
		t.Errorf("report %+v", r)
	}
	if v, ok := r.Float("temp"); !ok || v != 24.5 {
		t.Errorf("report temp %+v", r.Fields)
	}
}

// The BTHome component: devices and sensors added with the typed calls,
// an encrypted BLU advertisement heard, statuses, a button event over the
// WebSocket channel, discovery, a relay script's ble.scan_result event and
// a bthomedevice webhook.
func TestBTHomeAgainstSim(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const gw = "shelly1pmminig3-84fce63fe001"
	s.Add(sim.Device{Gen: 3, ID: gw, MAC: "84FCE63FE001", Model: "S3SW-001P8EU", App: "Mini1PMG3", FW: "1.4.4",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}, "sys": {"uptime": 1.0}}})
	c := client(s, gw, "")
	k := newHookSink(t, "")
	const addr = "3c:2e:f5:71:d5:2a"
	keyHex := "231d39c1d7cc1ab1aee224cd096db932"
	key, _ := hex.DecodeString(keyHex)
	name := "Door"
	dk, did, err := shelly.BTHomeAddDevice(ctx(t), c, shelly.BTHomeDeviceConfig{Addr: addr, Name: &name, Key: keyHex}, 0)
	if err != nil || dk != "bthomedevice:200" {
		t.Fatal(dk, err)
	}
	if _, _, err := shelly.BTHomeAddSensor(ctx(t), c, shelly.BTHomeSensorConfig{Addr: addr, ObjID: 0x2D}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := shelly.BTHomeAddSensor(ctx(t), c, shelly.BTHomeSensorConfig{Addr: addr, ObjID: 0x05}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := shelly.BTHomeAddSensor(ctx(t), c, shelly.BTHomeSensorConfig{Addr: "aa:aa:aa:aa:aa:aa", ObjID: 2}, 0); !shelly.IsRPCError(err, -105) {
		t.Errorf("sensor without device: %v", err)
	}
	if _, _, err := shelly.CreateWebhook(ctx(t), c, shelly.HookWebhook(k.base(), "door", "bthomedevice.single_push", did, map[string]string{"idx": "${ev.idx}"}, "")); err != nil {
		t.Fatal(err)
	}
	infos, err := shelly.BTHomeObjectInfos(ctx(t), c, []int{0x2D, 0x05})
	if err != nil || len(infos) != 2 || infos[0].Type != "binary_sensor" || infos[1].Unit != "lx" {
		t.Fatalf("infos %+v %v", infos, err)
	}

	// Follow notifications over the WebSocket channel.
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []shelly.Event
	ready := make(chan struct{})
	go func() {
		_ = shelly.WatchWS(wctx, "http://"+s.Addr(gw), shelly.WSOptions{Src: "test",
			Status: func(map[string]any) error { close(ready); return nil },
			Notify: func(method string, params json.RawMessage) error {
				if method != "NotifyEvent" {
					return nil
				}
				var p map[string]any
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				events = append(events, shelly.Events(p)...)
				mu.Unlock()
				return nil
			}})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("no WebSocket channel")
	}

	var b bthome.Builder
	b.Trigger = true
	sd, err := b.Add(0x00, 9).Add(0x01, 87).Add(0x05, 140.5).Add(0x2D, 1).Button(1).Firmware(1, 0, 21).Seal(key, addr, 100)
	if err != nil {
		t.Fatal(err)
	}
	s.Hear(gw, addr, -61, bthome.Advertising(sd, "SBDW-002C"))

	st, err := shelly.GetStatus(ctx(t), c)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := st.BTHomeDevice(200)
	if !ok || ds.Battery == nil || *ds.Battery != 87 || *ds.RSSI != -61 || *ds.PacketID != 9 || ds.FWVer != "v1.0.21" {
		t.Fatalf("device status %+v", ds)
	}
	if w, _ := st.BTHomeSensor(200); w.Value != true {
		t.Errorf("window %+v", w)
	}
	if l, _ := st.BTHomeSensor(201); l.Value != 140.5 {
		t.Errorf("lux %+v", l)
	}
	objs, err := shelly.BTHomeKnownObjects(ctx(t), c, 200)
	if err != nil || len(objs) < 5 {
		t.Fatalf("known %+v %v", objs, err)
	}
	waitFor(t, "the button event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			if ev, ok := shelly.ParseBTHomeDeviceEvent(e); ok && ev.Event == "single_push" && ev.Idx == 0 && ev.Channel == -1 {
				return true
			}
		}
		return false
	})
	if h := k.wait(t, 1); h[0].Event != "bthomedevice.single_push" || h[0].Fields.Get("idx") != "0" {
		t.Errorf("hook %+v", h[0])
	}

	// A replayed packet changes nothing; a wrong key is reported.
	s.SetComponent(gw, "bthomesensor:201", map[string]any{"value": 1.0})
	s.Hear(gw, addr, -61, bthome.Advertising(sd, "SBDW-002C"))
	if l, _ := shelly.GetStatus(ctx(t), c); func() any { v, _ := l.BTHomeSensor(201); return v.Value }() != 1.0 {
		t.Error("a replayed packet was applied")
	}

	// Discovery reports what the device heard.
	if err := shelly.BTHomeStartDiscovery(ctx(t), c, 5); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "discovery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			if d, ok := shelly.ParseBTHomeDiscovered(e); ok && d.Addr == addr && d.LocalName == "SBDW-002C" && d.Encrypted {
				return true
			}
		}
		return false
	})

	// A relay script passes advertisements on as ble.scan_result events.
	id, err := shelly.CreateScript(ctx(t), c, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if err := shelly.PutScriptCode(ctx(t), c, id, shelly.BLERelayScript(false), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := shelly.StartScript(ctx(t), c, id); err != nil {
		t.Fatal(err)
	}
	var plain bthome.Builder
	psd, _ := plain.Add(0x02, 21.5).ServiceData()
	s.Hear(gw, "a4:c1:38:00:00:01", -70, bthome.Advertising(psd, "ATC_000001"))
	waitFor(t, "the relayed advertisement", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			advs, ok, err := shelly.ParseBLEScanEvent(e)
			if !ok || err != nil || len(advs) != 1 {
				continue
			}
			p, err := advs[0].BTHome(nil)
			if err == nil && advs[0].Addr == "A4:C1:38:00:00:01" && len(p.Objects) == 1 && p.Objects[0].Value == 21.5 {
				return true
			}
		}
		return false
	})

	if err := shelly.BTHomeDeleteDevice(ctx(t), c, 200); err != nil {
		t.Fatal(err)
	}
	if st, _ := shelly.GetStatus(ctx(t), c); len(st.Keys("bthomesensor")) != 0 {
		t.Errorf("sensors left: %v", st.Keys("bthomesensor"))
	}
}
