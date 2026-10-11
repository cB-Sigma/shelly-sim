// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly-sim/sim"
)

// The peer: the aioshelly package for Python at this version, in this
// image.
const (
	image            = "python:3.12.10-slim"
	aioshellyPackage = "aioshelly==13.34.1"
	bthomePackage    = "bthome-ble==3.24.0"
	wsPort           = "8123" // aioshelly's server for outbound WebSockets, in the container
)

var (
	peerOnce sync.Once
	peerName string
	peerOwn  bool
	peerErr  error
	coiotTo  string // the container's CoIoT socket, published on the host
	wsTo     string // the container's WebSocket server, published on the host
)

func TestMain(m *testing.M) {
	code := m.Run()
	if peerOwn {
		_ = exec.Command("docker", "container", "rm", "-f", peerName).Run()
	}
	os.Exit(code)
}

// needPeer starts the Python container once and installs aioshelly in it.
func needPeer(t *testing.T) string {
	t.Helper()
	if os.Getenv("SIGMA_SHELLY_INTEROP") != "1" {
		t.Skip("runs aioshelly in Docker; set SIGMA_SHELLY_INTEROP=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	peerOnce.Do(func() {
		td, err := filepath.Abs("testdata")
		if err != nil {
			peerErr = err
			return
		}
		peerName = fmt.Sprintf("sigma-shelly-aioshelly-%d", os.Getpid())
		out, err := exec.Command("docker", "run", "-d", "--name", peerName,
			"--add-host", "host.docker.internal:host-gateway",
			"-p", "127.0.0.1::5683/udp", "-p", "127.0.0.1::"+wsPort+"/tcp",
			"-v", td+":/sigma:ro", image, "sleep", "3600").CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("docker run: %v: %s", err, out)
			return
		}
		peerOwn = true
		if coiotTo, peerErr = published(peerName, "5683/udp"); peerErr != nil {
			return
		}
		if wsTo, peerErr = published(peerName, wsPort+"/tcp"); peerErr != nil {
			return
		}
		out, err = exec.Command("docker", "exec", peerName, "pip", "install", "--no-cache-dir",
			"--disable-pip-version-check", "--root-user-action=ignore", aioshellyPackage, bthomePackage).CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("pip install %s: %v: %s", aioshellyPackage, err, out)
		}
	})
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	return peerName
}

// published is the host address of a container port.
func published(name, port string) (string, error) {
	out, err := exec.Command("docker", "port", name, port).Output()
	if err != nil {
		return "", fmt.Errorf("docker port %s: %v", port, err)
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if _, _, err := net.SplitHostPort(line); err != nil {
		return "", fmt.Errorf("docker port %s: %q", port, out)
	}
	return line, nil
}

// listenHost is where the devices listen so that the container reaches
// them at host.docker.internal (see doc.go).
func listenHost() string {
	if runtime.GOOS == "darwin" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

func port(t *testing.T, addr string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type check struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// peer runs the script with args, calling on for each check as it comes
// (so the test can act on the script's "waiting" lines), and fails the
// test for each failed check.
func peer(t *testing.T, on func(check), args ...string) map[string]check {
	t.Helper()
	name := needPeer(t)
	cmd := exec.Command("docker", append([]string{"exec", name, "python", "-I", "/sigma/aioshelly_peer.py"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	got := map[string]check{}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		var ck check
		if json.Unmarshal(sc.Bytes(), &ck) != nil {
			t.Logf("peer: %s", sc.Text())
			continue
		}
		got[ck.Check] = ck
		if on != nil {
			on(ck)
		}
		if ck.OK {
			t.Logf("PASS %s: %s", ck.Check, ck.Detail)
		} else {
			t.Errorf("FAIL %s: %s", ck.Check, ck.Detail)
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		t.Errorf("aioshelly peer: %v\n%s", err, lastLines(stderr.String(), 40))
	}
	return got
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func want(t *testing.T, got map[string]check, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, ok := got[n]; !ok {
			t.Errorf("check %s did not run", n)
		}
	}
}

// Gen1: aioshelly's BlockDevice reads /shelly, /settings, /status and
// /cit/d over HTTP with basic authentication, takes values from the
// CoIoT messages the simulator sends to its socket, and sends relay, light,
// colour, firmware and reboot requests.
func TestGen1(t *testing.T) {
	needPeer(t)
	s := sim.NewHost(listenHost())
	defer s.Close()
	const relay, dimmer, rgbw = "98CDAC1F2001", "98CDAC1F2002", "98CDAC1F2003"
	const fw = "20230913-114008/v1.14.0-gcb84623"
	temp := 21.5
	s.Add(sim.Device{Gen: 1, MAC: relay, Model: "SHSW-PM", FW: fw, Name: "Pump", Password: "pw-gen1",
		Relays: []sim.Relay1{{On: true, Power: 60.5, TotalWm: 120000}}, Inputs: []sim.Input1{{Event: "S", Cnt: 1}}, TempC: &temp})
	s.Add(sim.Device{Gen: 1, MAC: dimmer, Model: "SHDM-2", FW: fw, Lights: []sim.Light1{{On: false, Brightness: 10}}})
	s.Add(sim.Device{Gen: 1, MAC: rgbw, Model: "SHRGBW2", FW: fw, Lights: []sim.Light1{{On: false, Color: true, Red: 255, Gain: 80}}})
	s.SetUpdate(relay, "20231107-162425/v1.14.1-rc1-g0617c15")

	// The devices send their CoIoT status to aioshelly's socket every
	// 250 ms, as real ones multicast it.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			for _, k := range []string{relay, dimmer, rgbw} {
				_ = s.SendCoIoT(k, coiotTo)
			}
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	got := peer(t, nil, "gen1", "--host", "host.docker.internal", "--password", "pw-gen1",
		"--relay", port(t, s.Addr(relay))+","+relay, "--dimmer", port(t, s.Addr(dimmer))+","+dimmer, "--rgbw", port(t, s.Addr(rgbw))+","+rgbw)
	close(stop)
	wg.Wait()
	want(t, got, "gen1_wrong_password", "gen1_initialize", "gen1_shelly", "gen1_settings_status", "gen1_cit_d_blocks", "gen1_coiot_values",
		"gen1_relay_off", "gen1_relay_answer", "gen1_coap_update", "gen1_ota_url", "gen1_reboot",
		"gen1_dimmer_initialize", "gen1_dimmer_block", "gen1_dimmer_brightness", "gen1_rgbw_initialize", "gen1_rgbw_mode", "gen1_rgbw_gain")

	// What the devices saw.
	if s.Gen1(relay).Relays[0].On {
		t.Error("the relay is still on")
	}
	if l := s.Gen1(dimmer).Lights[0]; !l.On || l.Brightness != 30 {
		t.Errorf("dimmer = %+v", l)
	}
	if l := s.Gen1(rgbw).Lights[0]; !l.On || l.Gain != 25 || l.Red != 255 {
		t.Errorf("rgbw = %+v", l)
	}
	if u := s.UpdateURL(relay); u != "http://files.example.invalid/fw.zip" {
		t.Errorf("update URL %q", u)
	}
	if s.Reboots() != 1 {
		t.Errorf("%d reboots", s.Reboots())
	}
	if s.AuthFailures() == 0 {
		t.Error("the wrong password was not seen")
	}
}

// Gen2+: aioshelly's RpcDevice over WebSocket with the authentication
// object (status, config, notifications, switch, cover, Wi-Fi, outbound
// WebSocket settings, firmware, an RPC error), JSON-RPC over HTTP with
// aiohttp's digest middleware, and a sleeping device's outbound WebSocket
// to aioshelly's server.
func TestGen2(t *testing.T) {
	needPeer(t)
	s := sim.NewHost(listenHost())
	defer s.Close()
	const sw, cover, sleeper = "shellyplus1pm-a8032ab1e001", "shellyplus2pm-a8032ab1e002", "shellyplusht-a8032ab1e003"
	s.Add(sim.Device{Gen: 2, ID: sw, MAC: "A8032AB1E001", Model: "SNSW-001P16EU", App: "Plus1PM", FW: "1.4.4", Name: "Porch", Password: "pw-gen2",
		Ws: map[string]any{"enable": false, "server": nil, "ssl_ca": "ca.pem"},
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false, "apower": 0.0}, "input:0": {"id": 0.0, "state": false},
			"sys": {"uptime": 5.0}}})
	s.Add(sim.Device{Gen: 2, ID: cover, MAC: "A8032AB1E002", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4",
		Components: map[string]map[string]any{"cover:0": {"id": 0.0, "state": "stopped", "current_pos": 40.0}, "sys": {"uptime": 7.0}}})
	s.Add(sim.Device{Gen: 2, ID: sleeper, MAC: "A8032AB1E003", Model: "SNSN-0013A", App: "PlusHT", FW: "1.4.4",
		Components: map[string]map[string]any{"temperature:0": {"id": 0.0, "tC": 20.5}, "sys": {"uptime": 1.0, "wakeup_period": 600.0}}})
	s.SetUpdate(sw, "1.5.0")

	var closeOutbound func()
	got := peer(t, func(c check) {
		switch c.Check {
		case "waiting_for_event":
			go func() {
				for i := 0; i < 3; i++ {
					time.Sleep(300 * time.Millisecond)
					s.Event(sw, "input:0", "single_push")
				}
			}()
		case "waiting_for_outbound":
			time.Sleep(200 * time.Millisecond)
			f, err := s.DialOutbound(sleeper, "ws://"+wsTo+"/api/shelly/ws")
			if err != nil {
				t.Errorf("outbound WebSocket: %v", err)
				return
			}
			closeOutbound = f
		}
	}, "gen2", "--host", "host.docker.internal", "--password", "pw-gen2", "--ws-port", wsPort, "--server-ip", "192.0.2.10",
		"--switch", port(t, s.Addr(sw))+",A8032AB1E001", "--cover", port(t, s.Addr(cover))+",A8032AB1E002", "--sleeper", "A8032AB1E003")
	if closeOutbound != nil {
		closeOutbound()
	}
	want(t, got, "gen2_http_digest_wrong_password", "gen2_http_digest_0", "gen2_http_digest_1", "gen2_http_digest_2", "gen2_http_no_auth_challenge",
		"gen2_ws_wrong_password", "gen2_ws_initialize", "gen2_device_info", "gen2_config_name", "gen2_status", "gen2_switch_set", "gen2_notify_status",
		"gen2_notify_event", "gen2_wifi_setconfig", "gen2_outbound_ws_config", "gen2_outbound_ws_restart", "gen2_poll", "gen2_ota_stable", "gen2_rpc_error",
		"gen2_cover_initialize", "gen2_cover_position", "gen2_cover_status", "gen2_cover_value", "gen2_outbound_ws_online")

	// What the devices saw.
	if s.Component(sw, "switch:0")["output"] != true {
		t.Error("the switch is not on")
	}
	if w := s.WiFi(sw); w != (sim.STA{SSID: "Site", Password: "password1", Enabled: true}) {
		t.Errorf("wifi = %+v", w)
	}
	if ws := s.WsConfig(sw); ws["enable"] != true || ws["server"] != "ws://192.0.2.10:"+wsPort+"/api/shelly/ws" || ws["ssl_ca"] != "*" {
		t.Errorf("ws config = %v", ws)
	}
	if s.Reboots() != 1 || s.UpdateURL(sw) != "stage:stable" {
		t.Errorf("%d reboots, update %q", s.Reboots(), s.UpdateURL(sw))
	}
	if s.Component(cover, "cover:0")["current_pos"] != 70.0 {
		t.Errorf("cover = %v", s.Component(cover, "cover:0"))
	}
	if s.AuthFailures() == 0 {
		t.Error("the wrong passwords were not seen")
	}
}

// v0.2.0 additions through aioshelly: Shelly.ListMethods (its script
// support check), scripts, KVS, virtual components with GetComponents,
// schedules and webhooks through call_rpc, per-component configuration,
// a Gen3 and a Gen4 device, a Pro 3EM, and a cover with a travel time
// whose NotifyStatus frames aioshelly follows to closed.
func TestGen2More(t *testing.T) {
	needPeer(t)
	s := sim.NewHost(listenHost())
	defer s.Close()
	const sw, cover = "shellyplus1pm-a8032ab1e101", "shellyplus2pm-a8032ab1e102"
	s.Add(sim.Device{Gen: 2, ID: sw, MAC: "A8032AB1E101", Model: "SNSW-001P16EU", App: "Plus1PM", FW: "1.4.4", Name: "Pump", Password: "pw-more",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false, "apower": 0.0}, "sys": {"uptime": 5.0}}})
	s.Add(sim.Device{Gen: 2, ID: cover, MAC: "A8032AB1E102", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4", CoverTravel: 2 * time.Second,
		Components: map[string]map[string]any{"cover:0": {"id": 0.0, "state": "open", "current_pos": 100.0}, "sys": {"uptime": 7.0}}})
	g3 := s.Add(sim.Device{Gen: 3, ID: "shelly1pmg3-a8032ab1e103", MAC: "A8032AB1E103", Model: "S3SW-001P16EU", App: "S1PMG3", FW: "1.4.4",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false, "apower": 0.0}, "sys": {"uptime": 1.0}}})
	g4 := s.Add(sim.Device{Gen: 4, ID: "shelly1pmg4-a8032ab1e104", MAC: "A8032AB1E104", Model: "S4SW-001P16EU", App: "S1PMG4", FW: "1.6.1",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false, "apower": 0.0}, "sys": {"uptime": 1.0}}})
	em := s.Add(sim.Device{Gen: 2, ID: "shellypro3em-a8032ab1e105", MAC: "A8032AB1E105", Model: "SPEM-003CEBEU", App: "Pro3EM", FW: "1.4.4",
		Components: map[string]map[string]any{"em:0": {"id": 0.0, "a_act_power": 905.2, "b_act_power": 480.1, "c_act_power": 190.0, "total_act_power": 1575.3},
			"emdata:0": {"id": 0.0, "total_act": 317630.7}, "sys": {"uptime": 9.0}}})
	got := peer(t, nil, "gen2more", "--host", "host.docker.internal", "--password", "pw-more", "--ws-port", wsPort,
		"--switch", port(t, s.Addr(sw))+",A8032AB1E101", "--cover", port(t, s.Addr(cover))+",A8032AB1E102",
		"--gen3", port(t, g3)+",A8032AB1E103", "--gen4", port(t, g4)+",A8032AB1E104", "--meter", port(t, em)+",A8032AB1E105")
	want(t, got, "more_initialize", "more_switch_config", "more_supports_scripts", "more_script_create", "more_script_list", "more_script_putcode",
		"more_script_getcode", "more_script_start", "more_script_eval", "more_script_eval_result", "more_script_stop", "more_kvs_set", "more_kvs_get",
		"more_kvs_value", "more_virtual_add", "more_number_set", "more_dynamic_components", "more_dynamic_status", "more_number_range",
		"more_schedule_create", "more_schedule_list", "more_schedule_listed", "more_webhook_create", "more_webhook_id",
		"more_gen3_initialize", "more_gen3_gen", "more_gen3_switch", "more_gen4_initialize", "more_gen4_gen", "more_gen4_switch",
		"more_em_initialize", "more_em_values", "more_cover_initialize", "more_cover_close", "more_cover_timed")
	if code, _ := s.ScriptCode(sw, 1); !strings.HasPrefix(code, "// sigma interop") {
		t.Errorf("script code %q", code)
	}
	if v := s.KVS(sw)["sigma"]; v == nil {
		t.Error("KVS entry missing")
	}
}
