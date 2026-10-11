// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The rest of the Gen2+ RPC API: per-component configuration
// (<Type>.GetConfig and SetConfig, with defaults in the shape devices
// answer), scripts, schedules, webhooks (fired on switch, cover and input
// changes), the key-value store, dynamic (virtual) components, input
// triggers and counter resets, and timed cover movement.

// gen2Ext is a Gen2+ device's state beyond its components.
type gen2Ext struct {
	confOver   map[string]map[string]any // SetConfig changes by component key
	cfgRev     int
	scripts    map[int]*script
	nextScript int
	jobs       []map[string]any
	nextJob    int
	schedRev   int
	hooks      []map[string]any
	nextHook   int
	hookRev    int
	kvs        map[string]kvsEntry
	kvsRev     int
	virtual    map[string]map[string]any // dynamic component key → config
	coverStop  map[string]chan struct{}  // a moving cover's stop signal
	hookCalls  []string                  // webhook URLs called, in order
	sinks      map[int]func(method string, params map[string]any)
	nextSink   int
}

type script struct {
	name    string
	enable  bool
	running bool
	code    string
}

type kvsEntry struct {
	value any
	etag  string
}

func (d *device) ext() *gen2Ext {
	if d.x == nil {
		d.x = &gen2Ext{confOver: map[string]map[string]any{}, scripts: map[int]*script{}, nextScript: 1, nextJob: 1, nextHook: 1,
			kvs: map[string]kvsEntry{}, virtual: map[string]map[string]any{}, coverStop: map[string]chan struct{}{},
			sinks: map[int]func(string, map[string]any){}, cfgRev: 10}
	}
	return d.x
}

// Hooks lists the URLs the device's webhooks called, in order.
func (s *Sim) Hooks(key string) []string {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == nil || d.x == nil {
		return nil
	}
	return append([]string(nil), d.x.hookCalls...)
}

// KVS returns a copy of a Gen2+ device's key-value store.
func (s *Sim) KVS(key string) map[string]any {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	if d != nil && d.x != nil {
		for k, e := range d.x.kvs {
			out[k] = e.value
		}
	}
	return out
}

// ScriptCode returns the code of a device's script, and whether it runs.
func (s *Sim) ScriptCode(key string, id int) (code string, running bool) {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == nil || d.x == nil || d.x.scripts[id] == nil {
		return "", false
	}
	return d.x.scripts[id].code, d.x.scripts[id].running
}

// RunSchedule runs schedule job id's calls now, as the device does when
// its time comes, and returns how many calls failed.
func (s *Sim) RunSchedule(key string, id int) int {
	d := s.find(key)
	if d == nil {
		return 0
	}
	s.mu.Lock()
	var calls []any
	for _, j := range d.ext().jobs {
		if jid, _ := j["id"].(int); jid == id {
			calls, _ = j["calls"].([]any)
		}
	}
	s.mu.Unlock()
	failed := 0
	for _, c := range calls {
		m, _ := c.(map[string]any)
		method, _ := m["method"].(string)
		p, _ := m["params"].(map[string]any)
		if _, e := s.call(d, method, clone(p)); e != nil {
			failed++
		}
	}
	return failed
}

// ---- configuration --------------------------------------------------------------

func clone(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// merge copies src into dst, recursing into objects.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
			dst[k] = clone(sm)
			continue
		}
		dst[k] = v
	}
}

// defaultConfig is a component's configuration as a device in factory
// state answers it (the common fields of each type; s.mu held).
func defaultConfig(d *device, key string) map[string]any {
	typ, n, hasID := strings.Cut(key, ":")
	id, _ := strconv.Atoi(n)
	st := d.cfg.Components[key]
	_, metered := st["apower"]
	base := map[string]any{}
	if hasID {
		base["id"], base["name"] = id, nil
	}
	switch typ {
	case "switch":
		merge(base, map[string]any{"in_mode": "follow", "initial_state": "match_input", "auto_on": false, "auto_on_delay": 60.0,
			"auto_off": false, "auto_off_delay": 60.0})
		if metered {
			merge(base, map[string]any{"power_limit": 4480.0, "voltage_limit": 280.0, "current_limit": 16.0, "autorecover_voltage_errors": false})
		}
	case "cover":
		travel := d.cfg.CoverTravel.Seconds()
		if travel == 0 {
			travel = 60
		}
		merge(base, map[string]any{"in_mode": "dual", "initial_state": "stopped", "invert_directions": false, "swap_inputs": false,
			"maxtime_open": travel, "maxtime_close": travel, "power_limit": 1200.0, "voltage_limit": 280.0, "current_limit": 5.0,
			"motor":                 map[string]any{"idle_power_thr": 2.0, "idle_confirm_period": 0.25},
			"obstruction_detection": map[string]any{"enable": false, "direction": "both", "action": "stop", "power_thr": 1000.0, "holdoff": 1.0},
			"safety_switch":         map[string]any{"enable": false, "direction": "both", "action": "stop", "allowed_move": nil}})
	case "input":
		merge(base, map[string]any{"type": "switch", "enable": true, "invert": false, "factory_reset": true})
		if _, ok := st["percent"]; ok {
			merge(base, map[string]any{"type": "analog", "report_thr": 1.0, "range_map": nil})
		}
		if _, ok := st["counts"]; ok {
			merge(base, map[string]any{"type": "count", "count_rep_thr": 1000.0, "freq_window": 1.0, "freq_rep_thr": 1.0})
		}
	case "light", "rgb", "rgbw", "cct":
		merge(base, map[string]any{"in_mode": "follow", "initial_state": "restore_last", "auto_on": false, "auto_on_delay": 60.0,
			"auto_off": false, "auto_off_delay": 60.0, "transition_duration": 3.0, "min_brightness_on_toggle": 3.0,
			"night_mode": map[string]any{"enable": false, "brightness": 50.0, "active_between": []any{}},
			"default":    map[string]any{"brightness": 50.0}})
	case "temperature":
		merge(base, map[string]any{"report_thr_C": 0.5, "offset_C": 0.0})
	case "humidity":
		merge(base, map[string]any{"report_thr": 1.0, "offset": 0.0})
	case "em":
		merge(base, map[string]any{"blink_mode_selector": "active_energy", "phase_selector": "a", "monitor_phase_sequence": false, "ct_type": "120A"})
	case "em1":
		merge(base, map[string]any{"ct_type": "50A", "reverse": false})
	case "devicepower", "emdata", "em1data":
		delete(base, "name")
	}
	return base
}

// configLocked is Shelly.GetConfig (s.mu held): every component's
// configuration with the changes made to it, and the device-wide
// sections real devices always have.
func (s *Sim) configLocked(d *device) map[string]any {
	c := d.cfg
	x := d.ext()
	out := map[string]any{}
	for key := range c.Components {
		if key == "sys" || key == "wifi" || key == "cloud" || key == "mqtt" || key == "ble" || key == "ws" {
			continue
		}
		out[key] = defaultConfig(d, key)
	}
	for key, cfg := range x.virtual {
		out[key] = clone(cfg)
	}
	var name any
	if c.Name != "" {
		name = c.Name
	}
	out["sys"] = map[string]any{
		"device":   map[string]any{"name": name, "mac": c.MAC, "fw_id": "20241011-114455/" + c.FW + "-g8ab4e4a", "discoverable": true, "eco_mode": false},
		"location": map[string]any{"tz": "Asia/Kolkata", "lat": 28.6, "lon": 77.2},
		"debug":    map[string]any{"level": 2.0, "file_level": nil, "mqtt": map[string]any{"enable": false}, "websocket": map[string]any{"enable": false}, "udp": map[string]any{"addr": nil}},
		"ui_data":  map[string]any{},
		"rpc_udp":  map[string]any{"dst_addr": nil, "listen_port": nil},
		"sntp":     map[string]any{"server": "time.google.com"},
		"cfg_rev":  x.cfgRev,
	}
	var staSSID any
	if d.sta.SSID != "" {
		staSSID = d.sta.SSID
	}
	out["wifi"] = map[string]any{
		"ap":   map[string]any{"ssid": c.App + "-" + c.MAC, "is_open": true, "enable": false, "range_extender": map[string]any{"enable": false}},
		"sta":  map[string]any{"ssid": staSSID, "is_open": false, "enable": d.sta.Enabled, "ipv4mode": "dhcp", "ip": nil, "netmask": nil, "gw": nil, "nameserver": nil},
		"sta1": map[string]any{"ssid": nil, "is_open": true, "enable": false, "ipv4mode": "dhcp", "ip": nil, "netmask": nil, "gw": nil, "nameserver": nil},
		"roam": map[string]any{"rssi_thr": -80.0, "interval": 60.0},
	}
	out["cloud"] = map[string]any{"enable": false, "server": "iot.shelly.cloud:6012/jrpc"}
	out["mqtt"] = map[string]any{"enable": false, "server": nil, "client_id": c.ID, "user": nil, "ssl_ca": nil, "topic_prefix": c.ID,
		"rpc_ntf": true, "status_ntf": false, "use_client_cert": false, "enable_rpc": true, "enable_control": true}
	out["ble"] = map[string]any{"enable": true, "rpc": map[string]any{"enable": true}, "observer": map[string]any{"enable": false}}
	for key, over := range x.confOver {
		cur, ok := out[key].(map[string]any)
		if !ok {
			continue
		}
		merge(cur, over)
	}
	if c.Ws != nil {
		ws := map[string]any{}
		for k, v := range c.Ws {
			ws[k] = v
		}
		out["ws"] = ws
	}
	return out
}

// restartKeys are configuration changes a device applies only after a
// restart.
var restartKeys = map[string]bool{"mqtt": true, "ble": true, "cloud": true}

// setConfig is <Type>.SetConfig (s.mu not held).
func (s *Sim) setConfig(d *device, key string, cfg map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.configLocked(d)
	if _, ok := all[key]; !ok {
		return nil, rpcError(-105, "Argument 'id' not found!")
	}
	x := d.ext()
	if _, isVirtual := x.virtual[key]; isVirtual {
		merge(x.virtual[key], cfg)
	} else {
		if x.confOver[key] == nil {
			x.confOver[key] = map[string]any{}
		}
		merge(x.confOver[key], cfg)
	}
	if key == "sys" {
		if dev, ok := cfg["device"].(map[string]any); ok {
			if n, ok := dev["name"].(string); ok {
				d.cfg.Name = n
			}
		}
	}
	x.cfgRev++
	if sys := d.cfg.Components["sys"]; sys != nil {
		sys["cfg_rev"] = float64(x.cfgRev)
	}
	return map[string]any{"restart_required": restartKeys[key]}, nil
}

// ---- dispatch ---------------------------------------------------------------------

// callMore answers the methods of this file; handled is false for others.
func (s *Sim) callMore(d *device, method string, p map[string]any) (res any, rpcErr map[string]any, handled bool) {
	ns, op, _ := strings.Cut(method, ".")
	typ := strings.ToLower(ns)
	id, hasID := pnum(p, "id")
	key := typ
	if hasID {
		key += ":" + strconv.Itoa(int(id))
	}
	switch {
	case method == "Shelly.ListMethods":
		return map[string]any{"methods": s.methods(d)}, nil, true
	case op == "GetConfig" && typ != "shelly" && typ != "ws":
		s.mu.Lock()
		cfg, ok := s.configLocked(d)[key].(map[string]any)
		s.mu.Unlock()
		if !ok {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(id))+" not found!"), true
		}
		return cfg, nil, true
	case op == "SetConfig" && typ != "ws" && typ != "wifi":
		cfg, _ := p["config"].(map[string]any)
		if cfg == nil {
			return nil, rpcError(-103, "Missing required argument 'config'!"), true
		}
		r, e := s.setConfig(d, key, cfg)
		return r, e, true
	case ns == "Script":
		r, e := s.scriptCall(d, op, p)
		return r, e, true
	case ns == "Schedule":
		r, e := s.scheduleCall(d, op, p)
		return r, e, true
	case ns == "Webhook":
		r, e := s.webhookCall(d, op, p)
		return r, e, true
	case ns == "KVS":
		r, e := s.kvsCall(d, op, p)
		return r, e, true
	case ns == "Virtual":
		r, e := s.virtualCall(d, op, p)
		return r, e, true
	case op == "ResetCounters" && hasID:
		return s.resetCounters(d, key, p), nil, true
	case method == "Input.Trigger":
		ev, _ := p["event_type"].(string)
		if ev == "" {
			return nil, rpcError(-103, "Missing required argument 'event_type'!"), true
		}
		s.mu.Lock()
		_, ok := d.cfg.Components[key]
		s.mu.Unlock()
		if !ok {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(id))+" not found!"), true
		}
		go s.Event(d.key(), key, ev)
		return nil, nil, true
	case op == "Set" && hasID && s.isVirtual(d, key):
		r, e := s.virtualSet(d, key, p)
		return r, e, true
	case op == "GetStatus" && hasID && s.isVirtual(d, key):
		s.mu.Lock()
		defer s.mu.Unlock()
		return clone(d.cfg.Components[key]), nil, true
	}
	return nil, nil, false
}

// methods is Shelly.ListMethods: the device-wide methods and those of its
// component types, sorted.
func (s *Sim) methods(d *device) []string {
	set := map[string]bool{}
	for _, m := range []string{"Shelly.GetDeviceInfo", "Shelly.GetStatus", "Shelly.GetConfig", "Shelly.GetComponents", "Shelly.ListMethods",
		"Shelly.Update", "Shelly.Reboot", "Sys.GetStatus", "Sys.GetConfig", "Sys.SetConfig", "WiFi.GetStatus", "WiFi.GetConfig", "WiFi.SetConfig",
		"Cloud.GetConfig", "Cloud.SetConfig", "MQTT.GetConfig", "MQTT.SetConfig", "BLE.GetConfig", "BLE.SetConfig", "Ws.GetConfig", "Ws.SetConfig",
		"Script.List", "Script.Create", "Script.PutCode", "Script.GetCode", "Script.Start", "Script.Stop", "Script.Delete", "Script.Eval",
		"Script.GetStatus", "Script.GetConfig", "Script.SetConfig", "Schedule.Create", "Schedule.Update", "Schedule.List", "Schedule.Delete",
		"Schedule.DeleteAll", "Webhook.ListSupported", "Webhook.Create", "Webhook.Update", "Webhook.List", "Webhook.Delete", "Webhook.DeleteAll",
		"KVS.Set", "KVS.Get", "KVS.GetMany", "KVS.List", "KVS.Delete", "Virtual.Add", "Virtual.Delete"} {
		set[m] = true
	}
	s.mu.Lock()
	for key := range d.cfg.Components {
		typ, _, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		ns := map[string]string{"switch": "Switch", "cover": "Cover", "light": "Light", "rgb": "RGB", "rgbw": "RGBW", "cct": "CCT", "input": "Input",
			"em": "EM", "emdata": "EMData", "em1": "EM1", "em1data": "EM1Data", "pm1": "PM1", "temperature": "Temperature", "humidity": "Humidity",
			"devicepower": "DevicePower", "boolean": "Boolean", "number": "Number", "text": "Text", "enum": "Enum", "button": "Button", "group": "Group"}[typ]
		if ns == "" {
			continue
		}
		for _, op := range []string{"GetStatus", "GetConfig", "SetConfig"} {
			set[ns+"."+op] = true
		}
		switch typ {
		case "switch", "light", "rgb", "rgbw", "cct":
			set[ns+".Set"], set[ns+".Toggle"], set[ns+".ResetCounters"] = true, true, true
		case "cover":
			for _, op := range []string{"Open", "Close", "Stop", "GoToPosition", "ResetCounters"} {
				set["Cover."+op] = true
			}
		case "input":
			set["Input.Trigger"], set["Input.ResetCounters"] = true, true
		case "emdata", "em1data", "pm1":
			set[ns+".ResetCounters"] = true
		case "boolean", "number", "text", "enum", "button":
			set[ns+".Set"] = true
		}
	}
	s.mu.Unlock()
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// ---- scripts ------------------------------------------------------------------------

func (s *Sim) scriptCall(d *device, op string, p map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	fid, _ := pnum(p, "id")
	id := int(fid)
	sc := x.scripts[id]
	notFound := rpcError(-105, "Argument 'id', value "+strconv.Itoa(id)+" not found!")
	switch op {
	case "List":
		ids := make([]int, 0, len(x.scripts))
		for i := range x.scripts {
			ids = append(ids, i)
		}
		sort.Ints(ids)
		list := []any{}
		for _, i := range ids {
			v := x.scripts[i]
			list = append(list, map[string]any{"id": i, "name": v.name, "enable": v.enable, "running": v.running})
		}
		return map[string]any{"scripts": list}, nil
	case "Create":
		if len(x.scripts) >= 10 {
			return nil, rpcError(-109, "Too many scripts!")
		}
		name, _ := p["name"].(string)
		id := x.nextScript
		x.nextScript++
		x.scripts[id] = &script{name: name}
		d.cfg.Components["script:"+strconv.Itoa(id)] = map[string]any{"id": float64(id), "running": false, "errors": []any{}}
		return map[string]any{"id": id}, nil
	}
	if sc == nil {
		return nil, notFound
	}
	switch op {
	case "PutCode":
		code, ok := p["code"].(string)
		if !ok {
			return nil, rpcError(-103, "Missing required argument 'code'!")
		}
		if app, _ := p["append"].(bool); app {
			sc.code += code
		} else {
			sc.code = code
		}
		if len(sc.code) > 20*1024 {
			return nil, rpcError(-103, "Script too big!")
		}
		return map[string]any{"len": len(sc.code)}, nil
	case "GetCode":
		off := 0
		if o, ok := pnum(p, "offset"); ok {
			off = int(o)
		}
		if off > len(sc.code) {
			off = len(sc.code)
		}
		n := len(sc.code) - off
		if l, ok := pnum(p, "len"); ok && int(l) < n {
			n = int(l)
		}
		n = min(n, 2048) // the device answers in pages, whole characters each
		for off+n < len(sc.code) && n > 0 && !utf8.RuneStart(sc.code[off+n]) {
			n--
		}
		return map[string]any{"data": sc.code[off : off+n], "left": len(sc.code) - off - n}, nil
	case "Start", "Stop":
		was := sc.running
		sc.running = op == "Start"
		if comp := d.cfg.Components["script:"+strconv.Itoa(id)]; comp != nil {
			comp["running"] = sc.running
		}
		return map[string]any{"was_running": was}, nil
	case "Delete":
		if sc.running {
			return nil, rpcError(-103, "Script is running!")
		}
		delete(x.scripts, id)
		delete(d.cfg.Components, "script:"+strconv.Itoa(id))
		return nil, nil
	case "Eval":
		if !sc.running {
			return nil, rpcError(-103, "Script is not running!")
		}
		code, _ := p["code"].(string)
		return map[string]any{"result": evalSimple(code)}, nil
	case "GetStatus":
		return map[string]any{"id": id, "running": sc.running, "mem_used": 1024 + len(sc.code), "mem_peak": 2048 + len(sc.code), "mem_free": 25000, "errors": []any{}}, nil
	case "GetConfig":
		return map[string]any{"id": id, "name": sc.name, "enable": sc.enable}, nil
	case "SetConfig":
		cfg, _ := p["config"].(map[string]any)
		if n, ok := cfg["name"].(string); ok {
			sc.name = n
		}
		if e, ok := cfg["enable"].(bool); ok {
			sc.enable = e
		}
		return map[string]any{"restart_required": false}, nil
	}
	return nil, rpcError(-114, "Method Script."+op+" failed: No handler!")
}

// evalSimple stands in for the script engine: a number, a quoted string
// or a sum of numbers is evaluated; anything else answers "undefined".
func evalSimple(code string) string {
	c := strings.TrimSpace(code)
	if len(c) >= 2 && (c[0] == '"' || c[0] == '\'') && c[len(c)-1] == c[0] {
		return c[1 : len(c)-1]
	}
	sum := 0.0
	for _, part := range strings.Split(c, "+") {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return "undefined"
		}
		sum += v
	}
	return strconv.FormatFloat(sum, 'f', -1, 64)
}

// ---- schedules -----------------------------------------------------------------------

// validTimespec checks the shape of a timespec: six fields, or a sunrise
// or sunset form with five more fields.
func validTimespec(ts string) bool {
	f := strings.Fields(ts)
	if len(f) == 0 {
		return false
	}
	if strings.HasPrefix(f[0], "@sunrise") || strings.HasPrefix(f[0], "@sunset") {
		return len(f) <= 4
	}
	return len(f) == 6
}

func (s *Sim) scheduleCall(d *device, op string, p map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	bump := func() {
		x.schedRev++
		if sys := d.cfg.Components["sys"]; sys != nil {
			sys["schedule_rev"] = float64(x.schedRev)
		}
	}
	job := func() (map[string]any, map[string]any) {
		ts, _ := p["timespec"].(string)
		if !validTimespec(ts) {
			return nil, rpcError(-103, "Invalid argument 'timespec'!")
		}
		calls, ok := p["calls"].([]any)
		if !ok || len(calls) == 0 || len(calls) > 5 {
			return nil, rpcError(-103, "Invalid argument 'calls'!")
		}
		for _, c := range calls {
			if m, ok := c.(map[string]any); !ok || m["method"] == nil {
				return nil, rpcError(-103, "Invalid argument 'calls'!")
			}
		}
		en, _ := p["enable"].(bool)
		return map[string]any{"enable": en, "timespec": ts, "calls": calls}, nil
	}
	fid, _ := pnum(p, "id")
	id := int(fid)
	find := func() int {
		for i, j := range x.jobs {
			if j["id"] == id {
				return i
			}
		}
		return -1
	}
	switch op {
	case "Create":
		if len(x.jobs) >= 20 {
			return nil, rpcError(-109, "Too many jobs!")
		}
		j, e := job()
		if e != nil {
			return nil, e
		}
		j["id"] = x.nextJob
		x.nextJob++
		x.jobs = append(x.jobs, j)
		bump()
		return map[string]any{"id": j["id"], "rev": x.schedRev}, nil
	case "Update":
		i := find()
		if i < 0 {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(id)+" not found!")
		}
		cur := x.jobs[i]
		for _, k := range []string{"enable", "timespec", "calls"} {
			if v, ok := p[k]; ok {
				cur[k] = v
			}
		}
		if ts, _ := cur["timespec"].(string); !validTimespec(ts) {
			return nil, rpcError(-103, "Invalid argument 'timespec'!")
		}
		bump()
		return map[string]any{"rev": x.schedRev}, nil
	case "List":
		list := []any{}
		for _, j := range x.jobs {
			list = append(list, clone(j))
		}
		return map[string]any{"jobs": list, "rev": x.schedRev}, nil
	case "Delete":
		i := find()
		if i < 0 {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(id)+" not found!")
		}
		x.jobs = append(x.jobs[:i], x.jobs[i+1:]...)
		bump()
		return map[string]any{"rev": x.schedRev}, nil
	case "DeleteAll":
		x.jobs = nil
		bump()
		return map[string]any{"rev": x.schedRev}, nil
	}
	return nil, rpcError(-114, "Method Schedule."+op+" failed: No handler!")
}

// ---- webhooks ------------------------------------------------------------------------

// supportedEvents are the webhook events of a device's components.
func supportedEvents(d *device) []string {
	var out []string
	for key, st := range d.cfg.Components {
		typ, _, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		switch typ {
		case "switch":
			out = append(out, "switch.on", "switch.off")
		case "cover":
			out = append(out, "cover.open", "cover.closed", "cover.stopped", "cover.opening", "cover.closing")
		case "light", "rgb", "rgbw", "cct":
			out = append(out, typ+".on", typ+".off")
		case "input":
			if _, ok := st["state"]; ok {
				out = append(out, "input.toggle_on", "input.toggle_off", "input.button_push", "input.button_longpush",
					"input.button_doublepush", "input.button_triplepush")
			}
		case "temperature":
			out = append(out, "temperature.change")
		case "humidity":
			out = append(out, "humidity.change")
		}
	}
	sort.Strings(out)
	uniq := out[:0]
	for i, e := range out {
		if i == 0 || out[i-1] != e {
			uniq = append(uniq, e)
		}
	}
	return uniq
}

func (s *Sim) webhookCall(d *device, op string, p map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	bump := func() {
		x.hookRev++
		if sys := d.cfg.Components["sys"]; sys != nil {
			sys["webhook_rev"] = float64(x.hookRev)
		}
	}
	fid, _ := pnum(p, "id")
	id := int(fid)
	find := func() int {
		for i, h := range x.hooks {
			if h["id"] == id {
				return i
			}
		}
		return -1
	}
	check := func(h map[string]any) map[string]any {
		ev, _ := h["event"].(string)
		ok := false
		for _, e := range supportedEvents(d) {
			ok = ok || e == ev
		}
		if !ok {
			return rpcError(-103, "Invalid argument 'event'!")
		}
		urls, isList := h["urls"].([]any)
		if !isList || len(urls) > 5 {
			return rpcError(-103, "Invalid argument 'urls'!")
		}
		cid, _ := h["cid"].(float64)
		typ, _, _ := strings.Cut(ev, ".")
		if _, ok := d.cfg.Components[typ+":"+strconv.Itoa(int(cid))]; !ok {
			return rpcError(-105, "Argument 'cid', value "+strconv.Itoa(int(cid))+" not found!")
		}
		return nil
	}
	switch op {
	case "ListSupported":
		types := map[string]any{}
		for _, e := range supportedEvents(d) {
			types[e] = map[string]any{}
		}
		return map[string]any{"types": types}, nil
	case "Create":
		if len(x.hooks) >= 20 {
			return nil, rpcError(-109, "Too many webhooks!")
		}
		h := clone(p)
		if _, ok := h["cid"]; !ok {
			h["cid"] = 0.0
		}
		if _, ok := h["enable"]; !ok {
			h["enable"] = true
		}
		if e := check(h); e != nil {
			return nil, e
		}
		h["id"] = x.nextHook
		x.nextHook++
		x.hooks = append(x.hooks, h)
		bump()
		return map[string]any{"id": h["id"], "rev": x.hookRev}, nil
	case "Update":
		i := find()
		if i < 0 {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(id)+" not found!")
		}
		h := clone(x.hooks[i])
		for k, v := range p {
			if k != "id" {
				h[k] = v
			}
		}
		if e := check(h); e != nil {
			return nil, e
		}
		h["id"] = id
		x.hooks[i] = h
		bump()
		return map[string]any{"rev": x.hookRev}, nil
	case "List":
		list := []any{}
		for _, h := range x.hooks {
			list = append(list, clone(h))
		}
		return map[string]any{"hooks": list, "rev": x.hookRev}, nil
	case "Delete":
		i := find()
		if i < 0 {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(id)+" not found!")
		}
		x.hooks = append(x.hooks[:i], x.hooks[i+1:]...)
		bump()
		return map[string]any{"rev": x.hookRev}, nil
	case "DeleteAll":
		x.hooks = nil
		bump()
		return map[string]any{"rev": x.hookRev}, nil
	}
	return nil, rpcError(-114, "Method Webhook."+op+" failed: No handler!")
}

// hookHTTP is the client webhooks are called with.
var hookHTTP = &http.Client{Timeout: 3 * time.Second}

// fire calls the URLs of the enabled webhooks for event on component id
// (s.mu not held), in the background as a device does.
func (s *Sim) fire(d *device, event string, cid int) {
	s.mu.Lock()
	if d.x == nil {
		s.mu.Unlock()
		return
	}
	var urls []string
	for _, h := range d.x.hooks {
		hc, _ := h["cid"].(float64)
		if h["event"] != event || int(hc) != cid || h["enable"] != true {
			continue
		}
		list, _ := h["urls"].([]any)
		for _, u := range list {
			if us, ok := u.(string); ok && us != "" {
				urls = append(urls, us)
			}
		}
	}
	d.x.hookCalls = append(d.x.hookCalls, urls...)
	s.mu.Unlock()
	for _, u := range urls {
		go func(u string) {
			resp, err := hookHTTP.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}(u)
	}
}

// changed fires the webhooks of a component change: an output going on
// or off, a cover reaching a state.
func (s *Sim) changed(d *device, key string, before, after map[string]any) {
	typ, n, ok := strings.Cut(key, ":")
	if !ok {
		return
	}
	id, _ := strconv.Atoi(n)
	if o, has := after["output"].(bool); has && before["output"] != after["output"] {
		s.fire(d, typ+"."+map[bool]string{true: "on", false: "off"}[o], id)
	}
	if typ == "cover" {
		if st, has := after["state"].(string); has && before["state"] != st {
			s.fire(d, "cover."+st, id)
		}
	}
	if typ == "input" {
		if st, has := after["state"].(bool); has && before["state"] != after["state"] {
			s.fire(d, "input.toggle_"+map[bool]string{true: "on", false: "off"}[st], id)
		}
	}
}

// inputHook is the webhook event of an input event.
var inputHook = map[string]string{"single_push": "input.button_push", "long_push": "input.button_longpush",
	"double_push": "input.button_doublepush", "triple_push": "input.button_triplepush"}

// ---- KVS -----------------------------------------------------------------------------

func etagOf(v any) string {
	b, _ := json.Marshal(v)
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])[:16]
}

func (s *Sim) kvsCall(d *device, op string, p map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	bump := func() {
		x.kvsRev++
		if sys := d.cfg.Components["sys"]; sys != nil {
			sys["kvs_rev"] = float64(x.kvsRev)
		}
	}
	key, _ := p["key"].(string)
	etag, _ := p["etag"].(string)
	match := func(k string) bool {
		m, _ := p["match"].(string)
		if m == "" {
			return true
		}
		ok, _ := path.Match(m, k)
		return ok
	}
	sorted := func() []string {
		var ks []string
		for k := range x.kvs {
			if match(k) {
				ks = append(ks, k)
			}
		}
		sort.Strings(ks)
		return ks
	}
	switch op {
	case "Set":
		if key == "" || len(key) > 42 {
			return nil, rpcError(-103, "Invalid argument 'key'!")
		}
		v, ok := p["value"]
		if !ok {
			return nil, rpcError(-103, "Missing required argument 'value'!")
		}
		if b, _ := json.Marshal(v); len(b) > 253 {
			return nil, rpcError(-103, "Invalid argument 'value'!")
		}
		if cur, ok := x.kvs[key]; etag != "" && (!ok || cur.etag != etag) {
			return nil, rpcError(-103, "etag mismatch")
		}
		if _, ok := x.kvs[key]; !ok && len(x.kvs) >= 50 {
			return nil, rpcError(-109, "Too many keys!")
		}
		e := kvsEntry{value: v, etag: etagOf([]any{v, x.kvsRev + 1, key})}
		x.kvs[key] = e
		bump()
		return map[string]any{"etag": e.etag, "rev": x.kvsRev}, nil
	case "Get":
		e, ok := x.kvs[key]
		if !ok {
			return nil, rpcError(-105, "Argument 'key', value '"+key+"' not found!")
		}
		return map[string]any{"etag": e.etag, "value": e.value}, nil
	case "GetMany":
		ks := sorted()
		off := 0
		if o, ok := pnum(p, "offset"); ok {
			off = min(int(o), len(ks))
		}
		page := ks[off:min(off+10, len(ks))]
		items := []any{}
		for _, k := range page {
			items = append(items, map[string]any{"key": k, "etag": x.kvs[k].etag, "value": x.kvs[k].value})
		}
		return map[string]any{"items": items, "offset": off, "total": len(ks)}, nil
	case "List":
		keys := map[string]any{}
		for _, k := range sorted() {
			keys[k] = map[string]any{"etag": x.kvs[k].etag}
		}
		return map[string]any{"keys": keys, "rev": x.kvsRev}, nil
	case "Delete":
		cur, ok := x.kvs[key]
		if !ok {
			return nil, rpcError(-105, "Argument 'key', value '"+key+"' not found!")
		}
		if etag != "" && cur.etag != etag {
			return nil, rpcError(-103, "etag mismatch")
		}
		delete(x.kvs, key)
		bump()
		return map[string]any{"rev": x.kvsRev}, nil
	}
	return nil, rpcError(-114, "Method KVS."+op+" failed: No handler!")
}

// ---- virtual components ----------------------------------------------------------------

func (s *Sim) isVirtual(d *device, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.x == nil {
		return false
	}
	_, ok := d.x.virtual[key]
	return ok
}

var virtualTypes = map[string]any{"boolean": false, "number": 0.0, "text": "", "enum": nil, "group": []any{}, "button": nil}

func (s *Sim) virtualCall(d *device, op string, p map[string]any) (any, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	switch op {
	case "Add":
		typ, _ := p["type"].(string)
		initial, ok := virtualTypes[typ]
		if !ok {
			return nil, rpcError(-103, "Invalid argument 'type'!")
		}
		id := 200
		if f, ok := pnum(p, "id"); ok {
			id = int(f)
			if id < 200 || id > 299 {
				return nil, rpcError(-103, "Invalid argument 'id'!")
			}
		} else {
			for x.virtual[typ+":"+strconv.Itoa(id)] != nil {
				id++
			}
		}
		key := typ + ":" + strconv.Itoa(id)
		if x.virtual[key] != nil {
			return nil, rpcError(-103, "Component "+key+" already exists!")
		}
		cfg := map[string]any{"id": id, "name": nil, "meta": nil}
		if c, ok := p["config"].(map[string]any); ok {
			merge(cfg, c)
		}
		if typ == "enum" {
			if opts, ok := cfg["options"].([]any); ok && len(opts) > 0 {
				initial = opts[0]
			}
		}
		if dv, ok := cfg["default_value"]; ok {
			initial = dv
		}
		x.virtual[key] = cfg
		st := map[string]any{"id": float64(id)}
		if typ != "button" {
			st["value"] = initial
		}
		d.cfg.Components[key] = st
		x.cfgRev++
		return map[string]any{"id": id}, nil
	case "Delete":
		key, _ := p["key"].(string)
		if x.virtual[key] == nil {
			return nil, rpcError(-105, "Argument 'key', value '"+key+"' not found!")
		}
		delete(x.virtual, key)
		delete(d.cfg.Components, key)
		x.cfgRev++
		return nil, nil
	}
	return nil, rpcError(-114, "Method Virtual."+op+" failed: No handler!")
}

func (s *Sim) virtualSet(d *device, key string, p map[string]any) (any, map[string]any) {
	v, ok := p["value"]
	typ, _, _ := strings.Cut(key, ":")
	if !ok && typ != "button" {
		return nil, rpcError(-103, "Missing required argument 'value'!")
	}
	s.mu.Lock()
	cfg := d.x.virtual[key]
	bad := false
	switch typ {
	case "boolean":
		_, isB := v.(bool)
		bad = !isB
	case "number":
		f, isN := v.(float64)
		bad = !isN || math.IsNaN(f)
		if mn, ok := cfg["min"].(float64); ok && isN && f < mn {
			bad = true
		}
		if mx, ok := cfg["max"].(float64); ok && isN && f > mx {
			bad = true
		}
	case "text":
		str, isS := v.(string)
		bad = !isS
		if ml, ok := cfg["max_len"].(float64); ok && isS && float64(len(str)) > ml {
			bad = true
		}
	case "enum":
		bad = true
		opts, _ := cfg["options"].([]any)
		for _, o := range opts {
			if o == v {
				bad = false
			}
		}
	case "button":
		s.mu.Unlock()
		go s.Event(d.key(), key, "single_push")
		return nil, nil
	}
	s.mu.Unlock()
	if bad {
		return nil, rpcError(-103, "Invalid argument 'value'!")
	}
	// The WebSocket channel that asked holds the device's notification
	// order (wsMu) until the answer is sent, so the change is notified
	// after it, as set does in call.
	go s.SetComponent(d.key(), key, map[string]any{"value": v})
	s.mu.Lock()
	d.cfg.Components[key]["value"] = v
	s.mu.Unlock()
	return nil, nil
}

// ---- counters ---------------------------------------------------------------------------

func (s *Sim) resetCounters(d *device, key string, p map[string]any) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := d.cfg.Components[key]
	if c == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range c {
		switch {
		case k == "aenergy" || k == "ret_aenergy":
			if m, ok := v.(map[string]any); ok {
				out[k] = map[string]any{"total": m["total"]}
				m["total"] = 0.0
			}
		case strings.HasSuffix(k, "_energy") || k == "total_act" || k == "total_act_ret":
			out[k] = v
			c[k] = 0.0
		case k == "counts":
			if m, ok := v.(map[string]any); ok {
				out[k] = map[string]any{"total": m["total"]}
				m["total"] = 0.0
			}
		}
	}
	return out
}

// ---- cover timing ------------------------------------------------------------------------

// moveCover moves a cover to target percent: at once when the device has
// no travel time, else at 100 % per CoverTravel with a status every
// 200 ms, as opening or closing, then open, closed or stopped. A new move
// or Cover.Stop ends the one under way.
func (s *Sim) moveCover(d *device, key string, id float64, target float64, set func(string, map[string]any)) {
	s.mu.Lock()
	x := d.ext()
	if stop := x.coverStop[key]; stop != nil {
		close(stop)
		delete(x.coverStop, key)
	}
	travel := d.cfg.CoverTravel
	cur, _ := d.cfg.Components[key]["current_pos"].(float64)
	s.mu.Unlock()
	final := "stopped"
	if target >= 100 {
		final = "open"
	} else if target <= 0 {
		final = "closed"
	}
	if travel <= 0 || cur == target {
		set(key, map[string]any{"state": final, "current_pos": target})
		return
	}
	dir := "opening"
	last := "open"
	if target < cur {
		dir, last = "closing", "close"
	}
	stop := make(chan struct{})
	s.mu.Lock()
	x.coverStop[key] = stop
	s.mu.Unlock()
	set(key, map[string]any{"state": dir, "target_pos": target, "last_direction": last, "move_started_at": float64(time.Now().UnixMilli()) / 1000})
	go func() {
		step := 200 * time.Millisecond
		perStep := 100 * step.Seconds() / travel.Seconds()
		pos := cur
		t := time.NewTicker(step)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			if dir == "opening" {
				pos = math.Min(target, pos+perStep)
			} else {
				pos = math.Max(target, pos-perStep)
			}
			pos = math.Round(pos*10) / 10
			if pos == target {
				s.mu.Lock()
				if x.coverStop[key] == stop {
					delete(x.coverStop, key)
				}
				s.mu.Unlock()
				s.SetComponent(d.key(), key, map[string]any{"id": id, "state": final, "current_pos": pos, "target_pos": nil})
				return
			}
			s.SetComponent(d.key(), key, map[string]any{"id": id, "current_pos": pos})
		}
	}()
}

// stopCover ends a timed movement; ok is false when none was under way.
func (s *Sim) stopCover(d *device, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	stop := x.coverStop[key]
	if stop == nil {
		return false
	}
	close(stop)
	delete(x.coverStop, key)
	return true
}

// ---- notification sinks ---------------------------------------------------------------

// addSink registers fn to receive the device's notifications (MQTT); the
// returned function removes it.
func (s *Sim) addSink(d *device, fn func(method string, params map[string]any)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	n := x.nextSink
	x.nextSink++
	x.sinks[n] = fn
	return func() {
		s.mu.Lock()
		delete(x.sinks, n)
		s.mu.Unlock()
	}
}

func fmtID(id float64) string { return fmt.Sprint(int(id)) }

// gen1SettingsCall is a Gen1 /settings/<section>/<n> or /settings/actions
// request (s.mu held): query values are kept and the section is answered
// with them. Actions keep their slots by name, with URLs as given.
func gen1SettingsCall(d *device, parts []string, q url.Values) map[string]any {
	if d.g1 == nil {
		d.g1 = map[string]map[string]any{}
	}
	if parts[0] == "actions" {
		acts := d.g1["actions"]
		if acts == nil {
			acts = map[string]any{}
			if len(d.cfg.Relays) > 0 {
				for _, n := range []string{"out_on_url", "out_off_url", "btn_on_url", "btn_off_url", "shortpush_url", "longpush_url"} {
					acts[n] = []any{map[string]any{"index": 0.0, "enabled": false, "urls": []any{}}}
				}
			}
			for _, n := range []string{"btn1_shortpush_url", "btn1_longpush_url"} {
				if len(d.cfg.Lights) > 0 {
					acts[n] = []any{map[string]any{"index": 0.0, "enabled": false, "urls": []any{}}}
				}
			}
			d.g1["actions"] = acts
		}
		if name := q.Get("name"); name != "" {
			urls := []any{}
			for _, u := range q["urls[]"] {
				if u != "" {
					urls = append(urls, u)
				}
			}
			idx, _ := strconv.Atoi(q.Get("index"))
			acts[name] = []any{map[string]any{"index": float64(idx), "enabled": q.Get("enabled") == "true", "urls": urls}}
		}
		return map[string]any{"actions": acts}
	}
	key := strings.Join(parts, "/")
	cur := d.g1[key]
	if cur == nil {
		cur = map[string]any{"name": nil}
		d.g1[key] = cur
	}
	for k := range q {
		v := q.Get(k)
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cur[k] = f
		} else if b, err := strconv.ParseBool(v); err == nil {
			cur[k] = b
		} else {
			cur[k] = v
		}
	}
	return clone(cur)
}
