// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchWS(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const id = "shellyplus1pm-a8032ab10d01"
	s.Add(sim.Device{Gen: 2, ID: id, MAC: "A8032AB10D01", Model: "SNSW-001P16EU", App: "Plus1PM", Password: "pw",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false, "apower": 0.0}, "input:0": {"id": 0.0, "state": false}}})
	base := "http://" + s.Addr(id)

	// A wrong password ends with ErrAuth.
	err := shelly.WatchWS(ctx(t), base, shelly.WSOptions{Src: "test", Password: "nope", Timeout: 2 * time.Second})
	if !errors.Is(err, shelly.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}

	var mu sync.Mutex
	var status map[string]any
	var notes []string
	var events []shelly.Event
	wctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- shelly.WatchWS(wctx, base, shelly.WSOptions{Src: "test", Password: "pw", Timeout: 2 * time.Second, Ping: 50 * time.Millisecond,
			Status: func(st map[string]any) error { mu.Lock(); status = st; mu.Unlock(); return nil },
			Notify: func(method string, params json.RawMessage) error {
				var p map[string]any
				_ = json.Unmarshal(params, &p)
				mu.Lock()
				notes = append(notes, method)
				if method == "NotifyEvent" {
					events = append(events, shelly.Events(p)...)
				}
				mu.Unlock()
				return nil
			}})
	}()
	waitFor(t, "status and an authenticated channel", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return status != nil && s.WSClients(id) == 1
	})
	s.SetComponent(id, "switch:0", map[string]any{"output": true, "apower": 12.5})
	s.Event(id, "input:0", "single_push")
	waitFor(t, "the notifications", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(notes) >= 2 && len(events) == 1
	})
	mu.Lock()
	if events[0].Component != "input:0" || events[0].Name != "single_push" || events[0].TS <= 0 {
		t.Errorf("event = %+v", events[0])
	}
	if sw, _ := status["switch:0"].(map[string]any); sw["output"] != false {
		t.Errorf("status = %v", status)
	}
	mu.Unlock()
	// A dropped channel ends the watch with an error.
	s.DropWS(id)
	select {
	case err := <-done:
		if err == nil {
			t.Error("a dropped channel ended without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not notice the drop")
	}
	cancel()
	if shelly.WSURL("https://h:8443") != "wss://h:8443/rpc" || shelly.WSURL("http://h") != "ws://h/rpc" {
		t.Error("WSURL")
	}
}

func TestInbound(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const id = "shellyplusht-a8032ab10e01"
	s.Add(sim.Device{Gen: 2, ID: id, MAC: "A8032AB10E01", Model: "SNSN-0013A", App: "PlusHT",
		Ws:         map[string]any{"enable": false, "server": nil, "ssl_ca": "ca.pem"},
		Components: map[string]map[string]any{"temperature:0": {"id": 0.0, "tC": 21.5}}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := "ws://" + ln.Addr().String() + "/shelly"

	// Point the device at the server.
	c := client(s, id, "")
	cfg, err := c.OutboundWS(ctx(t))
	if err != nil || cfg.Enable || cfg.Server != "" {
		t.Fatalf("config = %+v %v", cfg, err)
	}
	restart, err := c.SetOutboundWS(ctx(t), server)
	if err != nil || !restart {
		t.Fatalf("set = %v %v", restart, err)
	}
	if err := c.Reboot(ctx(t)); err != nil || s.Reboots() != 1 {
		t.Fatalf("reboot: %v, %d", err, s.Reboots())
	}
	if cfg, _ := c.OutboundWS(ctx(t)); !cfg.Enable || cfg.Server != server || s.WsConfig(id)["ssl_ca"] != "*" {
		t.Errorf("config after = %+v", s.WsConfig(id))
	}

	var mu sync.Mutex
	var frames []shelly.Frame
	closed := 0
	sctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- shelly.ServeInbound(sctx, ln, "/shelly", &shelly.Inbound{
			Frame: func(_ context.Context, ch *shelly.InboundChannel, f shelly.Frame) {
				mu.Lock()
				defer mu.Unlock()
				if ch.RemoteIP != "127.0.0.1" {
					t.Errorf("remote %q", ch.RemoteIP)
				}
				ch.Value = f.Src
				frames = append(frames, f)
			},
			Closed: func(ch *shelly.InboundChannel) {
				mu.Lock()
				defer mu.Unlock()
				if ch.Value == id {
					closed++
				}
			}})
	}()
	closeWS, err := s.DialOutbound(id, server)
	if err != nil {
		t.Fatal(err)
	}
	s.SetComponent(id, "temperature:0", map[string]any{"tC": 19.0})
	waitFor(t, "two frames", func() bool { mu.Lock(); defer mu.Unlock(); return len(frames) == 2 })
	mu.Lock()
	if frames[0].Src != id || frames[0].Method != "NotifyFullStatus" || frames[1].Method != "NotifyStatus" {
		t.Errorf("frames = %+v", frames)
	}
	mu.Unlock()
	closeWS()
	waitFor(t, "the close", func() bool { mu.Lock(); defer mu.Unlock(); return closed == 1 })
	cancel()
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Errorf("serve ended with %v", err)
	}
}

func TestCoIoT(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const mac = "98CDAC1F0F01"
	temp, hum := 22.5, 40.0
	s.Add(sim.Device{Gen: 1, MAC: mac, Model: "SHSW-1", Relays: []sim.Relay1{{On: true, Power: 12, TotalWm: 600}, {On: false}},
		Inputs: []sim.Input1{{Event: "S", Cnt: 3}}, TempC: &temp, Hum: &hum})
	l, err := shelly.ListenCoIoT(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, "none")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan shelly.CoIoTMessage, 4)
	lctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- l.Serve(lctx, func(m shelly.CoIoTMessage, from *net.UDPAddr) {
			if !from.IP.IsLoopback() {
				t.Errorf("from %v", from)
			}
			got <- m
		})
	}()
	if err := s.SendCoIoT(mac, l.Addr()); err != nil {
		t.Fatal(err)
	}
	var m shelly.CoIoTMessage
	select {
	case m = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	if m.DevID != mac || m.DevType != "SHSW-1" || m.Path != "/cit/s" || m.Version != "2" || m.Serial == 0 {
		t.Errorf("message = %+v", m)
	}
	desc, err := client(s, mac, "").CitDescription(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]any{}
	for _, v := range desc.Decode(m.Payload) {
		k := v.Sensor.D
		if v.HasIndex {
			k += "/" + string(rune('0'+v.Index))
		} else if v.Sensor.U != "" {
			k += "/" + v.Sensor.U
		}
		vals[k] = v.Value
	}
	for k, want := range map[string]any{"output/0": 1.0, "output/1": 0.0, "power/0": 12.0, "energy/0": 600.0, "inputEvent/0": "S",
		"inputEventCnt/0": 3.0, "temperature/C": 22.5, "humidity/%": 40.0} {
		if vals[k] != want {
			t.Errorf("%s = %v, want %v (all %v)", k, vals[k], want, vals)
		}
	}
	if shelly.Gen1Event("ss") != "double_push" || shelly.Gen1Event("X") != "x" {
		t.Error("Gen1Event")
	}
	cancel()
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Errorf("serve ended with %v", err)
	}
	// The codec on its own: long options and garbage.
	enc := shelly.EncodeCoIoT("SHSW-25", "A4CF12F45B2E", 300, []byte(`{"G":[]}`))
	if m, err := shelly.ParseCoAP(enc); err != nil || m.Serial != 300 || m.Code != 30 || string(m.Payload) != `{"G":[]}` {
		t.Errorf("round trip %+v %v", m, err)
	}
	for _, bad := range [][]byte{nil, {0x80, 0, 0, 0}, {0x5f, 30, 0, 1}, {0x50, 30, 0, 1, 0xd1}} {
		if _, err := shelly.ParseCoAP(bad); err == nil {
			t.Errorf("% x parsed", bad)
		}
	}
}

// pub is an MQTT client stand-in: it hands what is published to the
// simulated devices and their answers back to the MQTT under test.
type pub struct {
	s   *sim.Sim
	m   *shelly.MQTT
	mu  sync.Mutex
	log []string
}

func (p *pub) Publish(_ context.Context, topic string, payload []byte, _ bool) error {
	p.mu.Lock()
	p.log = append(p.log, topic+" "+string(payload))
	p.mu.Unlock()
	if len(topic) > 4 && topic[len(topic)-4:] == "/rpc" {
		var req struct {
			ID     int            `json:"id"`
			Src    string         `json:"src"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(payload, &req)
		dev := topic[:len(topic)-4]
		if p.s.Addr(dev) == "" {
			return nil // nobody answers
		}
		res, e := p.s.RPC(dev, req.Method, req.Params)
		out := map[string]any{"id": req.ID, "src": dev, "dst": req.Src}
		if e != nil {
			out["error"] = e
		} else {
			out["result"] = res
		}
		b, _ := json.Marshal(out)
		go p.m.Handle(req.Src+"/rpc", b)
		return nil
	}
	const g1 = "shellies/shelly1-98CDAC1F1001/"
	if len(topic) > len(g1) && topic[:len(g1)] == g1 {
		p.s.MQTTCommand("98CDAC1F1001", topic[len(g1):], string(payload))
	}
	return nil
}

func TestMQTT(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const g2 = "shellyplus1-a8032ab11002"
	s.Add(sim.Device{Gen: 2, ID: g2, MAC: "A8032AB11002", Model: "SNSW-001X16EU", App: "Plus1",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}}})
	temp := 21.0
	s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F1001", Model: "SHSW-PM", Relays: []sim.Relay1{{On: false, Power: 5, TotalWm: 120}},
		Inputs: []sim.Input1{{Event: "L", Cnt: 2}}, TempC: &temp})
	m := &shelly.MQTT{Src: "gw", Timeout: 2 * time.Second}
	p := &pub{s: s, m: m}
	m.Pub = p
	if f := m.Filters(); len(f) != 4 || f[0] != "shellies/#" || f[3] != "gw/rpc" {
		t.Errorf("filters = %v", f)
	}
	// Gen2+: JSON-RPC over the broker, answers through Handle.
	if _, err := m.RPC(ctx(t), g2, "Switch.Set", map[string]any{"id": 0, "on": true}); err != nil {
		t.Fatal(err)
	}
	if s.Component(g2, "switch:0")["output"] != true {
		t.Error("switch not on")
	}
	if _, err := m.RPC(ctx(t), g2, "Switch.Set", map[string]any{"id": 4, "on": true}); err == nil || err.Error() != "the device refused Switch.Set: RPC error -105: Argument 'id', value 4 not found!" {
		t.Errorf("refused = %v", err)
	}
	if _, err := m.RPC(ctx(t), "nobody", "Switch.Set", nil); err == nil {
		t.Error("an unanswered request succeeded")
	}
	// Gen1: commands on the command topics.
	if err := m.Command1(ctx(t), "shellies/shelly1-98CDAC1F1001", shelly.Gen1Turn("relay", 0, "on")); err != nil {
		t.Fatal(err)
	}
	if !s.Gen1("98CDAC1F1001").Relays[0].On {
		t.Error("relay not on")
	}
	// Reading what devices publish.
	reports := map[string]shelly.Gen1Report{}
	for topic, payload := range s.Gen1Topics("98CDAC1F1001") {
		msg := m.Handle("shellies/shelly1-98CDAC1F1001/"+topic, []byte(payload))
		if msg.Kind != shelly.MQTTGen1 || msg.Device != "shelly1-98CDAC1F1001" || msg.Root != "shellies/shelly1-98CDAC1F1001" {
			t.Errorf("%s: %+v", topic, msg)
		}
		reports[topic] = msg.Report
	}
	if r := reports["relay/0"]; r.Kind != shelly.TopicRelay || !r.On {
		t.Errorf("relay = %+v", r)
	}
	if r := reports["relay/0/energy"]; r.Kind != shelly.TopicRelayEnergy || r.Num != 120 {
		t.Errorf("energy = %+v", r)
	}
	if r := reports["input_event/0"]; r.Kind != shelly.TopicInputEvent || r.Event != "L" || r.EventCnt != 2 {
		t.Errorf("input = %+v", r)
	}
	if r := reports["sensor/temperature"]; r.Kind != shelly.TopicSensor || r.Name != "temperature" || r.Num != 21 {
		t.Errorf("sensor = %+v", r)
	}
	if r := reports["online"]; r.Kind != shelly.TopicOnline || !r.Online {
		t.Errorf("online = %+v", r)
	}
	if r := shelly.ParseGen1Report([]string{"light", "1", "status"}, `{"ison":true,"brightness":40}`); r.Kind != shelly.TopicLightStatus || r.Index != 1 || r.Light["brightness"] != 40.0 {
		t.Errorf("light status = %+v", r)
	}
	if r := shelly.ParseGen1Report([]string{"roller", "0", "pos"}, "-1"); r.Kind != shelly.TopicRollerPos || r.Num != -1 {
		t.Errorf("roller pos = %+v", r)
	}
	n := m.Handle(g2+"/events/rpc", []byte(`{"src":"`+g2+`","method":"NotifyStatus","params":{"switch:0":{"output":false}}}`))
	if n.Kind != shelly.MQTTNotification || n.Src != g2 || n.Root != g2 || n.Method != "NotifyStatus" {
		t.Errorf("notification = %+v", n)
	}
	a := m.Handle("shellies/announce", []byte(`{"id":"shellyplug-s-98CDAC1F0A0A","model":"SHPLG-S","mac":"98CDAC1F0A0A","ip":"192.168.1.77"}`))
	if a.Kind != shelly.MQTTAnnounce || a.Announce.IP != "192.168.1.77" || a.Root != "shellies/shellyplug-s-98CDAC1F0A0A" {
		t.Errorf("announce = %+v", a)
	}
	if m.Handle("elsewhere/x", nil).Kind != shelly.MQTTNone || shelly.MACOfID("shelly1-A4CF12F45B2E") != "a4cf12f45b2e" || shelly.MACOfID("x") != "" {
		t.Error("other topics")
	}
}

// Lights are described in /cit/d and reported in CoIoT messages (they were
// missing, so a CoIoT client such as aioshelly found no light block).
func TestCoIoTLights(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const dim, rgbw = "98CDAC1F1201", "98CDAC1F1202"
	s.Add(sim.Device{Gen: 1, MAC: dim, Model: "SHDM-2", Lights: []sim.Light1{{On: true, Brightness: 65}}})
	s.Add(sim.Device{Gen: 1, MAC: rgbw, Model: "SHRGBW2", Lights: []sim.Light1{{On: false, Color: true, Red: 255, Green: 10, Blue: 0, Gain: 80}}})
	l, err := shelly.ListenCoIoT(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, "none")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan shelly.CoIoTMessage, 4)
	lctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Serve(lctx, func(m shelly.CoIoTMessage, _ *net.UDPAddr) { got <- m }) }()
	for key, want := range map[string]map[string]any{
		dim:  {"output": 1.0, "brightness": 65.0},
		rgbw: {"output": 0.0, "red": 255.0, "green": 10.0, "blue": 0.0, "gain": 80.0},
	} {
		if err := s.SendCoIoT(key, l.Addr()); err != nil {
			t.Fatal(err)
		}
		var m shelly.CoIoTMessage
		select {
		case m = <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("no message")
		}
		desc, err := client(s, key, "").CitDescription(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(desc.Blk) == 0 || desc.Blk[0].D != "light_0" {
			t.Errorf("%s blocks = %+v", key, desc.Blk)
		}
		vals := map[string]any{}
		for _, v := range desc.Decode(m.Payload) {
			if v.HasIndex && v.Index == 0 {
				vals[v.Sensor.D] = v.Value
			}
		}
		for k, w := range want {
			if vals[k] != w {
				t.Errorf("%s %s = %v, want %v (all %v)", key, k, vals[k], w, vals)
			}
		}
	}
}
