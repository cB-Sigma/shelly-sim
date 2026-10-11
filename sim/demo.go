// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// NewFullDemo returns NewDemo's six devices and, after them, one of each
// later kind: a Shelly 1PM Gen3, a Plug S Gen3, a 1PM Gen4, a Pro 3EM
// (three-phase meter), a PM Mini Gen3 (pm1), an EM Gen3 (two em1
// channels), a Plus i4 (four inputs), a Plus RGBW PM in rgbw mode and a
// Plus 2PM cover with a 15 s travel time. NewDemo stays as it was so
// tests that count its devices keep working.
func NewFullDemo() *Sim {
	s := NewDemo()
	sys := func(up float64) map[string]any { return map[string]any{"uptime": up} }
	meter := func(on bool, w, v, a, total float64) map[string]any {
		return map[string]any{"id": 0.0, "source": "init", "output": on, "apower": w, "voltage": v, "current": a, "freq": 50.0,
			"aenergy":     map[string]any{"total": total, "by_minute": []any{0.0, 0.0, 0.0}, "minute_ts": float64(time.Now().Unix())},
			"temperature": map[string]any{"tC": 38.5, "tF": 101.3}}
	}
	s.Add(Device{Gen: 3, ID: "shelly1pmg3-a8032ab10011", MAC: "A8032AB10011", Model: "S3SW-001P16EU", App: "S1PMG3", FW: "1.4.4", Name: "Borewell pump",
		Components: map[string]map[string]any{"switch:0": meter(false, 0, 232.1, 0, 5120.5), "input:0": {"id": 0.0, "state": false}, "sys": sys(120)}})
	s.Add(Device{Gen: 3, ID: "shellyplugsg3-a8032ab10012", MAC: "A8032AB10012", Model: "S3PL-00112EU", App: "PlugSG3", FW: "1.4.4", Name: "Kettle",
		Components: map[string]map[string]any{"switch:0": meter(true, 1840, 229.4, 8.0, 3300), "sys": sys(80)}})
	s.Add(Device{Gen: 4, ID: "shelly1pmg4-a8032ab10013", MAC: "A8032AB10013", Model: "S4SW-001P16EU", App: "S1PMG4", FW: "1.6.1", Name: "Porch",
		Components: map[string]map[string]any{"switch:0": meter(true, 12.4, 231.0, 0.06, 410), "input:0": {"id": 0.0, "state": true}, "sys": sys(15)}})
	s.Add(Device{Gen: 2, ID: "shellypro3em-a8032ab10014", MAC: "A8032AB10014", Model: "SPEM-003CEBEU", App: "Pro3EM", FW: "1.4.4", Name: "Main supply",
		Components: map[string]map[string]any{
			"em:0": {"id": 0.0, "a_current": 4.1, "a_voltage": 231.5, "a_act_power": 905.2, "a_aprt_power": 949.0, "a_pf": 0.95, "a_freq": 50.0,
				"b_current": 2.2, "b_voltage": 229.8, "b_act_power": 480.1, "b_aprt_power": 505.6, "b_pf": 0.95, "b_freq": 50.0,
				"c_current": 0.9, "c_voltage": 232.6, "c_act_power": 190.0, "c_aprt_power": 209.3, "c_pf": 0.91, "c_freq": 50.0,
				"n_current": nil, "total_current": 7.2, "total_act_power": 1575.3, "total_aprt_power": 1663.9},
			"emdata:0": {"id": 0.0, "a_total_act_energy": 182300.5, "a_total_act_ret_energy": 0.0, "b_total_act_energy": 95120.0,
				"b_total_act_ret_energy": 0.0, "c_total_act_energy": 40210.2, "c_total_act_ret_energy": 12.0, "total_act": 317630.7, "total_act_ret": 12.0},
			"temperature:0": {"id": 0.0, "tC": 44.1, "tF": 111.4}, "sys": sys(9000)}})
	s.Add(Device{Gen: 3, ID: "shellypmminig3-a8032ab10015", MAC: "A8032AB10015", Model: "S3PM-001PCEU16", App: "PMMiniG3", FW: "1.4.4", Name: "Air conditioner",
		Components: map[string]map[string]any{"pm1:0": {"id": 0.0, "voltage": 230.2, "current": 6.1, "apower": 1390.0, "freq": 50.0,
			"aenergy": map[string]any{"total": 81200.0}, "ret_aenergy": map[string]any{"total": 0.0}}, "sys": sys(600)}})
	s.Add(Device{Gen: 3, ID: "shellyemg3-a8032ab10016", MAC: "A8032AB10016", Model: "S3EM-002CXCEU", App: "EMG3", FW: "1.4.4", Name: "Solar and grid",
		Components: map[string]map[string]any{
			"em1:0":     {"id": 0.0, "current": 9.8, "voltage": 231.0, "act_power": -2200.0, "aprt_power": 2263.8, "pf": -0.97, "freq": 50.0},
			"em1:1":     {"id": 1.0, "current": 3.2, "voltage": 231.0, "act_power": 700.0, "aprt_power": 739.2, "pf": 0.95, "freq": 50.0},
			"em1data:0": {"id": 0.0, "total_act_energy": 120.0, "total_act_ret_energy": 410200.0},
			"em1data:1": {"id": 1.0, "total_act_energy": 88300.0, "total_act_ret_energy": 0.0},
			"switch:0":  {"id": 0.0, "output": false}, "sys": sys(3000)}})
	s.Add(Device{Gen: 2, ID: "shellyplusi4-a8032ab10017", MAC: "A8032AB10017", Model: "SNSN-0024X", App: "PlusI4", FW: "1.4.4", Name: "Hall switches",
		Components: map[string]map[string]any{"input:0": {"id": 0.0, "state": false}, "input:1": {"id": 1.0, "state": false},
			"input:2": {"id": 2.0, "state": false}, "input:3": {"id": 3.0, "state": false}, "sys": sys(44)}})
	s.Add(Device{Gen: 2, ID: "shellyplusrgbwpm-a8032ab10018", MAC: "A8032AB10018", Model: "SNDC-0D4P10WW", App: "PlusRGBWPM", FW: "1.4.4", Name: "Cove lighting",
		Components: map[string]map[string]any{"rgbw:0": {"id": 0.0, "source": "init", "output": true, "brightness": 60.0, "rgb": []any{255.0, 120.0, 0.0},
			"white": 0.0, "apower": 14.2, "voltage": 12.1, "current": 1.17, "aenergy": map[string]any{"total": 230.0}}, "sys": sys(70)}})
	s.Add(Device{Gen: 2, ID: "shellyplus2pm-a8032ab10019", MAC: "A8032AB10019", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4", Name: "Balcony shutter",
		CoverTravel: 15 * time.Second,
		Components: map[string]map[string]any{"cover:0": {"id": 0.0, "source": "init", "state": "stopped", "current_pos": 100.0, "apower": 0.0, "voltage": 231.0,
			"pos_control": true, "last_direction": "open", "aenergy": map[string]any{"total": 41.0}}, "input:0": {"id": 0.0, "state": false},
			"input:1": {"id": 1.0, "state": false}, "sys": sys(500)}})
	return s
}

// Service is a DNS-SD service a device announces on the network.
type Service struct {
	Instance string
	Service  string // _shelly._tcp or _http._tcp
	Host     string // host label, without .local
	Port     int
	TXT      map[string]string
	IP       net.IP
}

// Services lists what each device announces over mDNS: Gen2 and later
// devices _shelly._tcp (gen, app and ver in TXT) and _http._tcp with
// their id as the instance; Gen1 devices _http._tcp as shelly<type>-<MAC>.
// The port is the device's port here (80 on a real device). The module
// does not announce them itself, so that it keeps no mDNS dependency;
// hand them to an mDNS responder such as github.com/cB-Sigma/mdns (the
// interop module does this).
func (s *Sim) Services() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Service
	for _, d := range s.devs {
		host, p, err := net.SplitHostPort(strings.TrimPrefix(d.srv.URL, "http://"))
		if err != nil {
			continue
		}
		port, _ := strconv.Atoi(p)
		ip := net.ParseIP(host)
		c := d.cfg
		if c.Gen >= 2 {
			out = append(out,
				Service{Instance: c.ID, Service: "_shelly._tcp", Host: c.ID, Port: port, IP: ip,
					TXT: map[string]string{"gen": strconv.Itoa(c.Gen), "app": c.App, "ver": c.FW}},
				Service{Instance: c.ID, Service: "_http._tcp", Host: c.ID, Port: port, IP: ip, TXT: map[string]string{}})
			continue
		}
		name := "shelly" + strings.ToLower(gen1Kind(c.Model)) + "-" + c.MAC
		out = append(out, Service{Instance: name, Service: "_http._tcp", Host: strings.ToLower(name), Port: port, IP: ip, TXT: map[string]string{}})
	}
	return out
}

// gen1Kind is the short kind in a Gen1 host name (SHSW-1 → 1, SHSW-PM →
// 1pm, SHDM-2 → dimmer2); the type code itself otherwise.
func gen1Kind(model string) string {
	switch model {
	case "SHSW-1":
		return "1"
	case "SHSW-PM":
		return "1pm"
	case "SHSW-25":
		return "25"
	case "SHDM-2":
		return "dimmer2"
	case "SHPLG-S":
		return "plug-s"
	case "SHRGBW2":
		return "rgbw2"
	case "SHHT-1":
		return "ht"
	}
	return strings.ToLower(model)
}
