// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly-sim/sim"
	"github.com/coder/websocket"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDemo(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	keys := s.Keys()
	if len(keys) != 6 || keys[0] != "shellyplus2pm-a8032ab10001" || keys[5] != "98CDAC1F0006" {
		t.Fatalf("keys = %v", keys)
	}
	code, body := get(t, "http://"+s.Addr(keys[0])+"/shelly")
	if code != 200 || !strings.Contains(body, `"gen":2`) {
		t.Errorf("gen2 /shelly = %d %s", code, body)
	}
	code, body = get(t, "http://"+s.Addr("98CDAC1F0005")+"/relay/0?turn=off")
	if code != 200 || !strings.Contains(body, `"ison":false`) || s.Gen1("98CDAC1F0005").Relays[0].On {
		t.Errorf("relay = %d %s", code, body)
	}
	res, e := s.RPC(keys[2], "Switch.Toggle", map[string]any{"id": 0.0})
	if e != nil || s.Component(keys[2], "switch:0")["output"] != false {
		t.Errorf("toggle = %v %v", res, e)
	}
	s.SetOffline(keys[1], true)
	if code, _ := get(t, "http://"+s.Addr(keys[1])+"/shelly"); code != http.StatusServiceUnavailable {
		t.Errorf("offline device answered %d", code)
	}
	if s.Calls("/shelly") != 1 || len(s.Requests()) == 0 {
		t.Errorf("calls %d, requests %v", s.Calls("/shelly"), s.Requests())
	}
}

// Start the demo devices and read one.
func Example() {
	s := sim.NewDemo()
	defer s.Close()
	resp, err := http.Get("http://" + s.Addr("98CDAC1F0006") + "/status")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	var st struct {
		Lights []struct {
			IsOn       bool `json:"ison"`
			Brightness int  `json:"brightness"`
		} `json:"lights"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	fmt.Println(st.Lights[0].IsOn, st.Lights[0].Brightness)
	// Output: true 65
}

// What aioshelly asks while setting a device up, found missing when it was
// run against the simulator: Shelly.GetConfig (the device name under sys),
// Shelly.GetComponents for dynamic components, <Component>.GetStatus,
// Shelly.Update by release channel, the Gen1 /reboot and the Gen1 light
// mode in /settings.
func TestSetupCallsOfOtherClients(t *testing.T) {
	s := sim.NewDemo()
	defer s.Close()
	const plus, cover = "shellyplus2pm-a8032ab10001", "shellyplus2pm-a8032ab10002"
	res, e := s.RPC(plus, "Shelly.GetConfig", nil)
	cfg, _ := res.(map[string]any)
	sys, _ := cfg["sys"].(map[string]any)
	dev, _ := sys["device"].(map[string]any)
	if e != nil || dev["name"] != "Kitchen" || dev["mac"] != "A8032AB10001" || cfg["switch:1"] == nil {
		t.Errorf("config = %v %v", res, e)
	}
	res, e = s.RPC(plus, "Shelly.GetComponents", map[string]any{"dynamic_only": true})
	if comp, _ := res.(map[string]any); e != nil || comp["total"] != 0 || len(comp["components"].([]any)) != 0 {
		t.Errorf("dynamic components = %v %v", res, e)
	}
	res, e = s.RPC(plus, "Shelly.GetComponents", nil)
	if comp, _ := res.(map[string]any); e != nil || comp["total"] != 7 {
		t.Errorf("components = %v %v", res, e)
	}
	res, e = s.RPC(cover, "Cover.GetStatus", map[string]any{"id": 0.0})
	if st, _ := res.(map[string]any); e != nil || st["current_pos"] != 40.0 {
		t.Errorf("cover status = %v %v", res, e)
	}
	if _, e = s.RPC(cover, "Cover.GetStatus", map[string]any{"id": 3.0}); e == nil || e["code"] != -105 {
		t.Errorf("missing cover = %v", e)
	}
	if res, e = s.RPC(plus, "Sys.GetStatus", nil); e != nil || res.(map[string]any)["uptime"] != 1000.0 {
		t.Errorf("sys status = %v %v", res, e)
	}
	if _, e = s.RPC(plus, "Shelly.Update", map[string]any{"stage": "stable"}); e == nil {
		t.Error("an update without one on offer was accepted")
	}
	s.SetUpdate(plus, "1.5.0")
	if _, e = s.RPC(plus, "Shelly.Update", map[string]any{"stage": "stable"}); e != nil || s.UpdateURL(plus) != "stage:stable" {
		t.Errorf("stable update = %v, %q", e, s.UpdateURL(plus))
	}
	if code, _ := get(t, "http://"+s.Addr("98CDAC1F0005")+"/reboot"); code != 200 || s.Reboots() != 1 {
		t.Errorf("gen1 reboot = %d, %d reboots", code, s.Reboots())
	}
	rgbw := s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F1101", Model: "SHRGBW2", Lights: []sim.Light1{{On: true, Color: true}}})
	if _, body := get(t, "http://"+rgbw+"/settings"); !strings.Contains(body, `"mode":"color"`) {
		t.Errorf("rgbw settings = %s", body)
	}
	white := s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F1102", Model: "SHRGBW2", Lights: []sim.Light1{{On: true}}})
	if _, body := get(t, "http://"+white+"/settings"); !strings.Contains(body, `"mode":"white"`) {
		t.Errorf("white settings = %s", body)
	}
}

// NewHost devices listen on the given address.
func TestNewHost(t *testing.T) {
	s := sim.NewHost("127.0.0.1")
	defer s.Close()
	addr := s.Add(sim.Device{Gen: 2, ID: "shellyplus1-a8032ab1a001", MAC: "A8032AB1A001", Model: "SNSW-001X16EU", App: "Plus1",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}}})
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("addr = %s", addr)
	}
	if code, body := get(t, "http://"+addr+"/shelly"); code != 200 || !strings.Contains(body, `"gen":2`) {
		t.Errorf("/shelly = %d %s", code, body)
	}
}

// Over WebSocket, Shelly.GetDeviceInfo is answered without authentication
// (as over HTTP); other methods are challenged and an unauthenticated
// channel gets no notifications. aioshelly asks for the device info first
// and could not log in while the simulator challenged it.
func TestWebSocketDeviceInfoWithoutAuth(t *testing.T) {
	s := sim.New()
	defer s.Close()
	const id = "shellyplus1-a8032ab1a101"
	addr := s.Add(sim.Device{Gen: 2, ID: id, MAC: "A8032AB1A101", Model: "SNSW-001X16EU", App: "Plus1", Password: "pw",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	call := func(method string) map[string]any {
		b, _ := json.Marshal(map[string]any{"id": 1, "src": "test", "method": method})
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
		_, r, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(r, &out)
		return out
	}
	info := call("Shelly.GetDeviceInfo")
	if res, _ := info["result"].(map[string]any); res["id"] != id || res["auth_en"] != true {
		t.Errorf("device info = %v", info)
	}
	st := call("Shelly.GetStatus")
	if e, _ := st["error"].(map[string]any); e["code"] != 401.0 || !strings.Contains(e["message"].(string), `"realm":"`+id+`"`) {
		t.Errorf("status without auth = %v", st)
	}
	if n := s.WSClients(id); n != 0 {
		t.Errorf("%d authenticated channels", n)
	}
}
