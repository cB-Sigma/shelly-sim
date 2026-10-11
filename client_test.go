// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

const (
	plus2pm = "shellyplus2pm-a8032ab10001"
	plugS   = "shellyplusplugs-a8032ab10003"
	g1pm    = "98CDAC1F0005"
	g1dim   = "98CDAC1F0006"
)

func client(s *sim.Sim, key, password string) *shelly.Client {
	return &shelly.Client{Base: "http://" + s.Addr(key), Password: password, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestInfo(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	i2, err := client(s, plus2pm, "").Shelly(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if i2.Generation() != 2 || i2.DefaultID() != plus2pm || i2.ModelName() != "SNSW-102P16EU" || i2.Firmware() != "1.4.4" || i2.App != "Plus2PM" {
		t.Errorf("gen2 info = %+v", i2)
	}
	i1, err := client(s, g1pm, "").Shelly(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if i1.Generation() != 1 || i1.DefaultID() != "shelly-98cdac1f0005" || i1.ModelName() != "SHSW-PM" || !strings.HasSuffix(i1.Firmware(), "v1.14.0-gcb84623") {
		t.Errorf("gen1 info = %+v", i1)
	}
}

func TestGen1BasicAuth(t *testing.T) {
	s := sim.New()
	defer s.Close()
	s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F0A01", Model: "SHSW-1", Password: "pw", Relays: []sim.Relay1{{On: false}}})
	if _, err := client(s, "98CDAC1F0A01", "").Get1(ctx(t), "/status"); !errors.Is(err, shelly.ErrAuth) {
		t.Errorf("no password: %v", err)
	}
	if _, err := client(s, "98CDAC1F0A01", "wrong").Get1(ctx(t), "/status"); !errors.Is(err, shelly.ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	c := client(s, "98CDAC1F0A01", "pw")
	if _, err := c.Get1(ctx(t), shelly.Gen1Turn("relay", 0, "on")); err != nil {
		t.Fatal(err)
	}
	st, err := c.Get1(ctx(t), "/status")
	if err != nil {
		t.Fatal(err)
	}
	relays, _ := st["relays"].([]any)
	if r, _ := relays[0].(map[string]any); r["ison"] != true {
		t.Errorf("status = %v", st)
	}
	if _, err := c.Get1(ctx(t), "/nothing"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("unknown path: %v", err)
	}
}

func TestGen2DigestRPC(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const id = "shellyplus1-a8032ab10b01"
	s.Add(sim.Device{Gen: 2, ID: id, MAC: "A8032AB10B01", Model: "SNSW-001X16EU", App: "Plus1", Password: "secret",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}}})
	if _, err := client(s, id, "").RPC(ctx(t), "Shelly.GetStatus", nil); !errors.Is(err, shelly.ErrAuth) {
		t.Errorf("no password: %v", err)
	}
	if _, err := client(s, id, "nope").RPC(ctx(t), "Shelly.GetStatus", nil); !errors.Is(err, shelly.ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	c := client(s, id, "secret")
	// The first call answers the challenge; later ones reuse it with the
	// nonce count going up.
	for i := 0; i < 3; i++ {
		if _, err := c.RPC(ctx(t), "Switch.Set", map[string]any{"id": 0, "on": i%2 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := c.RPC(ctx(t), "Shelly.GetStatus", nil)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]map[string]any
	_ = json.Unmarshal(raw, &st)
	if st["switch:0"]["output"] != true {
		t.Errorf("status = %s", raw)
	}
	_, err = c.RPC(ctx(t), "Switch.Set", map[string]any{"id": 7, "on": true})
	var re *shelly.RPCError
	if !errors.As(err, &re) || re.Code != -105 {
		t.Errorf("rpc error = %v", err)
	}
	if s.AuthFailures() != 1 {
		t.Errorf("%d authentication failures, want 1 (the wrong password)", s.AuthFailures())
	}
}

// A light in colour mode (an RGBW2 or Bulb) dims with gain on /color/n;
// the simulated one, like the device, ignores a brightness on /light/n.
func TestGen1LevelColourMode(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const rgbw, dim = "98CDAC1F1101", "98CDAC1F1102"
	s.Add(sim.Device{Gen: 1, MAC: rgbw, Model: "SHRGBW2", Lights: []sim.Light1{{On: true, Color: true, Red: 255, Gain: 80}}})
	s.Add(sim.Device{Gen: 1, MAC: dim, Model: "SHDM-2", Lights: []sim.Light1{{On: true, Brightness: 65}}})
	if _, err := client(s, rgbw, "").Get1(ctx(t), shelly.Gen1Level(0, 25, true, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := client(s, dim, "").Get1(ctx(t), shelly.Gen1Level(0, 30, false, nil)); err != nil {
		t.Fatal(err)
	}
	if l := s.Gen1(rgbw).Lights[0]; l.Gain != 25 || !l.On {
		t.Errorf("rgbw = %+v", l)
	}
	if l := s.Gen1(dim).Lights[0]; l.Brightness != 30 {
		t.Errorf("dimmer = %+v", l)
	}
	// The white-mode form on a colour-mode light changes nothing.
	if _, err := client(s, rgbw, "").Get1(ctx(t), shelly.Gen1Level(0, 60, false, nil)); err != nil {
		t.Fatal(err)
	}
	if l := s.Gen1(rgbw).Lights[0]; l.Gain != 25 {
		t.Errorf("rgbw after a brightness = %+v", l)
	}
}

func TestSetWiFiAndBLE(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const id = "shellyplusi4-a8032ab10c01"
	s.Add(sim.Device{Gen: 2, ID: id, MAC: "A8032AB10C01", Model: "SNSN-0024X", App: "PlusI4",
		Components: map[string]map[string]any{"input:0": {"id": 0.0, "state": false}}})
	s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F0C02", Model: "SHSW-1", Relays: []sim.Relay1{{}}})
	if err := client(s, id, "").SetWiFi(ctx(t), 2, "Site", "password1"); err != nil {
		t.Fatal(err)
	}
	if err := client(s, "98CDAC1F0C02", "").SetWiFi(ctx(t), 1, "Site", "password2"); err != nil {
		t.Fatal(err)
	}
	if w := s.WiFi(id); w != (sim.STA{SSID: "Site", Password: "password1", Enabled: true}) {
		t.Errorf("gen2 sta = %+v", w)
	}
	if w := s.WiFi("98CDAC1F0C02"); w != (sim.STA{SSID: "Site", Password: "password2", Enabled: true}) {
		t.Errorf("gen1 sta = %+v", w)
	}
	// The same over BLE RPC: a long request written and an answer read in
	// 20-octet chunks.
	peer := s.BLE(id)
	raw, err := shelly.BLERPC(ctx(t), peer, "test", 1, "Shelly.GetDeviceInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	var info shelly.Info
	if json.Unmarshal(raw, &info) != nil || info.ID != id || peer.Writes < 3 {
		t.Errorf("info = %s after %d writes", raw, peer.Writes)
	}
	if _, err := shelly.BLERPC(ctx(t), peer, "test", 2, "WiFi.SetConfig", shelly.WiFiConfig("Other", "password3")); err != nil {
		t.Fatal(err)
	}
	if w := s.WiFi(id); w.SSID != "Other" || w.Password != "password3" {
		t.Errorf("sta over BLE = %+v", w)
	}
	var re *shelly.RPCError
	if _, err := shelly.BLERPC(ctx(t), peer, "test", 3, "No.Such", nil); !errors.As(err, &re) || re.Code != -114 {
		t.Errorf("BLE rpc error = %v", err)
	}
}

func TestFirmwareUpdate(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	s.SetUpdate(plus2pm, "1.5.0")
	s.SetUpdate(g1pm, "20250101-000000/v1.15.0-gabc")
	for _, tc := range []struct {
		key  string
		gen  int
		want string
	}{{plus2pm, 2, "1.5.0"}, {g1pm, 1, "v1.15.0"}} {
		c := client(s, tc.key, "")
		before, _ := c.Shelly(ctx(t))
		if err := c.Update(ctx(t), tc.gen, "http://files.example.invalid/fw.zip"); err != nil {
			t.Fatal(err)
		}
		info, err := c.WaitVersion(ctx(t), before.Firmware(), tc.want, 20*time.Millisecond)
		if err != nil || !strings.Contains(info.Firmware(), tc.want) {
			t.Errorf("%s: %+v %v", tc.key, info, err)
		}
		if s.UpdateURL(tc.key) != "http://files.example.invalid/fw.zip" {
			t.Errorf("%s fetched %q", tc.key, s.UpdateURL(tc.key))
		}
	}
	// A refused update, and a wait that ends with its context.
	c := client(s, plugS, "")
	if err := c.Update(ctx(t), 2, "http://files.example.invalid/fw.zip"); err == nil {
		t.Error("refused update succeeded")
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.WaitVersion(short, "1.4.4", "", 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wait = %v", err)
	}
}
