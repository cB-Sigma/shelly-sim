// Package sim simulates Shelly devices on their local APIs for tests
// and demonstrations: Gen1 devices on the HTTP API
// (/shelly, /status, /settings, /relay, /light, /roller, basic
// authentication) and Gen2+ devices on JSON-RPC over HTTP (POST /rpc,
// digest authentication with SHA-256) and over WebSocket (ws://host/rpc,
// with the JSON-RPC authentication object and NotifyStatus / NotifyEvent
// notifications). Each device is its own HTTP server on 127.0.0.1, or on
// the address given to NewHost. Built from Shelly's public API
// documentation; not a Shelly product.
package sim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Relay1 is a Gen1 relay output with its meter.
type Relay1 struct {
	On      bool
	Power   float64 // W
	TotalWm float64 // watt-minutes, as Gen1 counts
}

// Light1 is a Gen1 dimmer output. With Color set it is a Bulb or RGBW2
// in colour mode: it dims with gain on /color/n, and a brightness on
// /light/n is ignored there.
type Light1 struct {
	On               bool
	Brightness       int
	Color            bool
	Red, Green, Blue int
	Gain             int
}

// Roller1 is a Gen1 roller (cover) output.
type Roller1 struct {
	State string // open, close or stop (the direction of travel)
	Pos   int
}

// Device configures a simulated device.
type Device struct {
	Gen      int    // 1 or 2 (2 covers Gen2, Gen3 and Gen4, which share the RPC API)
	ID       string // Gen2: device id such as shellyplus2pm-a8032ab12345
	MAC      string // 12 upper-case hexadecimal digits
	Model    string // Gen2: SNSW-102P16EU; Gen1: type such as SHSW-PM
	App      string // Gen2: Plus2PM
	FW       string
	Name     string
	Password string // empty: no authentication
	// Gen2 state: component key ("switch:0") → status object.
	Components map[string]map[string]any
	// Gen1 state.
	Mode    string // relay or roller
	Relays  []Relay1
	Lights  []Light1
	Rollers []Roller1
	TempC   *float64
	Hum     *float64
	// Inputs are Gen1 inputs (buttons): the last event and its count.
	Inputs []Input1
	// Battery is a Gen1 battery device's charge (nil: mains powered).
	Battery *float64
	// Gen2: Ws is the outbound WebSocket configuration (Ws.GetConfig).
	Ws map[string]any
	// CoverTravel is how long a Gen2+ cover takes from closed to open;
	// zero moves it at once.
	CoverTravel time.Duration
}

// Input1 is a Gen1 input: the last event (S, L, SS …) and a count that
// goes up with each.
type Input1 struct {
	Event string
	Cnt   int
}

type device struct {
	cfg     Device
	nextFW  string // the version an update installs (SetUpdate)
	lastURL string // the URL of the last update asked for
	sta     STA
	srv     *httptest.Server
	offline bool
	nonces  map[string]bool
	wsNonce int64
	clients map[*websocket.Conn]string // authenticated channel → src
	// wsMu orders a channel's traffic as the device's single event loop
	// does: a reply is computed and sent, or a change is made and
	// notified, never interleaved, so a reply never carries a state older
	// than a notification sent before it.
	wsMu sync.Mutex
	x    *gen2Ext                  // scripts, schedules, webhooks, KVS, configuration changes
	g1   map[string]map[string]any // Gen1 settings sections and actions
}

// STA is the Wi-Fi a device was told to join.
type STA struct {
	SSID, Password string
	Enabled        bool
}

// SetUpdate makes an update of the device (Gen2+ Shelly.Update, Gen1
// /ota) install version v a moment later; with v empty an update is
// refused.
func (s *Sim) SetUpdate(key, v string) {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d != nil {
		d.nextFW = v
	}
}

// UpdateURL returns the URL of the last update the device was asked for.
func (s *Sim) UpdateURL(key string) string {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == nil {
		return ""
	}
	return d.lastURL
}

// update starts an update from url; ok is false when the device refuses.
func (s *Sim) update(d *device, url string) bool {
	s.mu.Lock()
	d.lastURL = url
	next := d.nextFW
	s.mu.Unlock()
	if next == "" || url == "" {
		return false
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.mu.Lock()
		d.cfg.FW = next
		s.mu.Unlock()
	}()
	return true
}

// WiFi returns the Wi-Fi a device was given (first-time setup).
func (s *Sim) WiFi(key string) STA {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == nil {
		return STA{}
	}
	return d.sta
}

// Sim is a set of simulated devices.
type Sim struct {
	mu       sync.Mutex
	devs     []*device
	calls    map[string]int
	authFail int
	requests []string
	reboots  int
	serials  map[string]int
	host     string // the IP address devices listen on; empty: 127.0.0.1
}

// New returns an empty simulator whose devices listen on 127.0.0.1.
func New() *Sim { return &Sim{calls: map[string]int{}} }

// NewHost returns an empty simulator whose devices listen on host (an IP
// address such as 0.0.0.0, for peers in containers or on other machines).
// Addr then gives host:port.
func NewHost(host string) *Sim { return &Sim{calls: map[string]int{}, host: host} }

func f(v float64) *float64 { return &v }

// NewDemo returns a simulator with a Shelly Plus 2PM as two switches, a
// Plus 2PM on a curtain (cover mode), a Plus Plug S, a Plus H&T, a Gen1
// Shelly 1PM and a Gen1 Dimmer 2.
func NewDemo() *Sim {
	s := New()
	s.Add(Device{Gen: 2, ID: "shellyplus2pm-a8032ab10001", MAC: "A8032AB10001", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4", Name: "Kitchen",
		Components: map[string]map[string]any{
			"switch:0": {"id": 0.0, "output": true, "apower": 820.5, "voltage": 231.2, "current": 3.6, "aenergy": map[string]any{"total": 15234.5}, "temperature": map[string]any{"tC": 41.2}},
			"switch:1": {"id": 1.0, "output": false, "apower": 0.0, "voltage": 231.2, "current": 0.0, "aenergy": map[string]any{"total": 802.0}},
			"input:0":  {"id": 0.0, "state": false}, "input:1": {"id": 1.0, "state": false},
			"sys": {"uptime": 1000.0}, "wifi": {"rssi": -60.0}, "cloud": {"connected": false},
		}})
	s.Add(Device{Gen: 2, ID: "shellyplus2pm-a8032ab10002", MAC: "A8032AB10002", Model: "SNSW-102P16EU", App: "Plus2PM", FW: "1.4.4", Name: "Bedroom curtain",
		Components: map[string]map[string]any{
			"cover:0": {"id": 0.0, "state": "stopped", "current_pos": 40.0, "apower": 0.0, "voltage": 230.1, "aenergy": map[string]any{"total": 120.0}},
			"input:0": {"id": 0.0, "state": false}, "input:1": {"id": 1.0, "state": false}, "sys": {"uptime": 50.0},
		}})
	s.Add(Device{Gen: 2, ID: "shellyplusplugs-a8032ab10003", MAC: "A8032AB10003", Model: "SNPL-00112EU", App: "PlugS", FW: "1.4.4", Name: "Geyser",
		Components: map[string]map[string]any{
			"switch:0": {"id": 0.0, "output": true, "apower": 1980.0, "voltage": 229.0, "current": 8.7, "aenergy": map[string]any{"total": 250000.0}},
			"sys":      {"uptime": 10.0},
		}})
	s.Add(Device{Gen: 2, ID: "shellyplusht-a8032ab10004", MAC: "A8032AB10004", Model: "SNSN-0013A", App: "PlusHT", FW: "1.4.4", Name: "Store room",
		Components: map[string]map[string]any{
			"temperature:0": {"id": 0.0, "tC": 29.4, "tF": 84.9}, "humidity:0": {"id": 0.0, "rh": 71.0},
			"devicepower:0": {"id": 0.0, "battery": map[string]any{"V": 5.9, "percent": 88.0}}, "sys": {"uptime": 3.0},
		}})
	s.Add(Device{Gen: 1, MAC: "98CDAC1F0005", Model: "SHSW-PM", FW: "20230913-114008/v1.14.0-gcb84623", Mode: "relay",
		Relays: []Relay1{{On: true, Power: 60.5, TotalWm: 120000}}})
	s.Add(Device{Gen: 1, MAC: "98CDAC1F0006", Model: "SHDM-2", FW: "20230913-114008/v1.14.0-gcb84623",
		Lights: []Light1{{On: true, Brightness: 65}}})
	return s
}

// Add starts a device and returns its address (127.0.0.1:port).
func (s *Sim) Add(d Device) string {
	dv := &device{cfg: d, nonces: map[string]bool{}, clients: map[*websocket.Conn]string{}}
	if dv.cfg.Components == nil {
		dv.cfg.Components = map[string]map[string]any{}
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(dv, w, r) })
	if s.host == "" {
		dv.srv = httptest.NewServer(h)
	} else {
		dv.srv = httptest.NewUnstartedServer(h)
		ln, err := net.Listen("tcp", net.JoinHostPort(s.host, "0"))
		if err != nil {
			panic(fmt.Sprintf("shelly sim: listen on %s: %v", s.host, err))
		}
		_ = dv.srv.Listener.Close()
		dv.srv.Listener = ln
		dv.srv.Start()
	}
	s.mu.Lock()
	s.devs = append(s.devs, dv)
	s.mu.Unlock()
	return strings.TrimPrefix(dv.srv.URL, "http://")
}

// Close stops every device.
func (s *Sim) Close() {
	s.mu.Lock()
	devs := append([]*device(nil), s.devs...)
	s.mu.Unlock()
	for _, d := range devs {
		s.DropWS(d.key())
		d.srv.Close()
	}
}

func (d *device) key() string {
	if d.cfg.ID != "" {
		return d.cfg.ID
	}
	return d.cfg.MAC
}

func (s *Sim) find(key string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devs {
		if d.key() == key || d.cfg.MAC == key {
			return d
		}
	}
	return nil
}

// Keys lists the devices' keys (the Gen2 id, or the Gen1 MAC) in the
// order they were added.
func (s *Sim) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.devs))
	for _, d := range s.devs {
		out = append(out, d.key())
	}
	return out
}

// Addr returns a device's address by Gen2 id or MAC.
func (s *Sim) Addr(key string) string {
	if d := s.find(key); d != nil {
		return strings.TrimPrefix(d.srv.URL, "http://")
	}
	return ""
}

// Calls counts requests by method (Gen2) or path (Gen1).
func (s *Sim) Calls(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[what]
}

// Requests lists the control requests received ("<id> Switch.Set {...}",
// "<mac> /relay/0?turn=off").
func (s *Sim) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// AuthFailures counts requests refused for authentication after a
// credential was offered.
func (s *Sim) AuthFailures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authFail
}

// WSClients counts authenticated WebSocket channels of a device.
func (s *Sim) WSClients(key string) int {
	d := s.find(key)
	if d == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(d.clients)
}

// DropWS closes a device's WebSocket channels (a Wi-Fi drop).
func (s *Sim) DropWS(key string) {
	d := s.find(key)
	if d == nil {
		return
	}
	s.mu.Lock()
	var cs []*websocket.Conn
	for c := range d.clients {
		cs = append(cs, c)
	}
	d.clients = map[*websocket.Conn]string{}
	s.mu.Unlock()
	for _, c := range cs {
		_ = c.CloseNow()
	}
}

// SetOffline makes a device answer 503 and drop its channels.
func (s *Sim) SetOffline(key string, off bool) {
	d := s.find(key)
	if d == nil {
		return
	}
	s.mu.Lock()
	d.offline = off
	s.mu.Unlock()
	if off {
		s.DropWS(key)
	}
}

// Component returns a copy of a Gen2 component's state.
func (s *Sim) Component(key, comp string) map[string]any {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	if d != nil {
		for k, v := range d.cfg.Components[comp] {
			out[k] = v
		}
	}
	return out
}

// SetComponent merges fields into a Gen2 component (as a button press or
// a load change would) and notifies the device's channels.
func (s *Sim) SetComponent(key, comp string, fields map[string]any) {
	d := s.find(key)
	if d == nil {
		return
	}
	d.wsMu.Lock()
	s.mu.Lock()
	c := d.cfg.Components[comp]
	if c == nil {
		c = map[string]any{}
		d.cfg.Components[comp] = c
	}
	before := map[string]any{}
	for k, v := range c {
		before[k] = v
	}
	for k, v := range fields {
		c[k] = v
	}
	after := map[string]any{}
	for k, v := range c {
		after[k] = v
	}
	s.mu.Unlock()
	delta := map[string]any{comp: fields}
	s.notifyLocked(d, "NotifyStatus", delta)
	d.wsMu.Unlock()
	s.changed(d, comp, before, after)
}

// Event sends a NotifyEvent for a component ("input:0", "single_push").
func (s *Sim) Event(key, comp, event string) {
	d := s.find(key)
	if d == nil {
		return
	}
	id, _ := strconv.Atoi(comp[strings.Index(comp, ":")+1:])
	ts := float64(time.Now().UnixMilli()) / 1000
	s.notify(d, "NotifyEvent", map[string]any{"ts": ts, "events": []any{map[string]any{"component": comp, "id": id, "event": event, "ts": ts}}})
	if h, ok := inputHook[event]; ok && strings.HasPrefix(comp, "input:") {
		s.fire(d, h, id)
	}
}

// Gen1 returns a copy of a Gen1 device's configuration and state.
func (s *Sim) Gen1(key string) Device {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == nil {
		return Device{}
	}
	out := d.cfg
	out.Relays = append([]Relay1(nil), d.cfg.Relays...)
	out.Lights = append([]Light1(nil), d.cfg.Lights...)
	out.Rollers = append([]Roller1(nil), d.cfg.Rollers...)
	return out
}

// SetGen1 changes a Gen1 device's state.
func (s *Sim) SetGen1(key string, fn func(*Device)) {
	if d := s.find(key); d != nil {
		s.mu.Lock()
		fn(&d.cfg)
		s.mu.Unlock()
	}
}

func (s *Sim) notify(d *device, method string, params map[string]any) {
	d.wsMu.Lock()
	defer d.wsMu.Unlock()
	s.notifyLocked(d, method, params)
}

// notifyLocked sends a notification to the device's channels (d.wsMu
// held).
func (s *Sim) notifyLocked(d *device, method string, params map[string]any) {
	if _, ok := params["ts"]; !ok {
		params["ts"] = float64(time.Now().UnixMilli()) / 1000
	}
	s.mu.Lock()
	type target struct {
		c   *websocket.Conn
		dst string
	}
	var ts []target
	for c, dst := range d.clients {
		ts = append(ts, target{c, dst})
	}
	src := d.key()
	var sinks []func(string, map[string]any)
	if d.x != nil {
		keys := make([]int, 0, len(d.x.sinks))
		for k := range d.x.sinks {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			sinks = append(sinks, d.x.sinks[k])
		}
	}
	s.mu.Unlock()
	for _, fn := range sinks {
		fn(method, params)
	}
	for _, t := range ts {
		b, _ := json.Marshal(map[string]any{"src": src, "dst": t.dst, "method": method, "params": params})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = t.c.Write(ctx, websocket.MessageText, b)
		cancel()
	}
}

func (s *Sim) count(what, logLine string) {
	s.mu.Lock()
	s.calls[what]++
	if logLine != "" {
		s.requests = append(s.requests, logLine)
	}
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Sim) serve(d *device, w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	off := d.offline
	s.mu.Unlock()
	if off {
		http.Error(w, "offline", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/shelly" {
		s.count("/shelly", "")
		writeJSON(w, 200, s.shellyInfo(d))
		return
	}
	if d.cfg.Gen >= 2 {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			s.serveWS(d, w, r)
			return
		}
		s.serveRPC(d, w, r)
		return
	}
	s.serveGen1(d, w, r)
}

func (s *Sim) shellyInfo(d *device) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := d.cfg
	if c.Gen >= 2 {
		var dom any
		if c.Password != "" {
			dom = c.ID
		}
		return map[string]any{"name": c.Name, "id": c.ID, "mac": c.MAC, "slot": 1, "model": c.Model, "gen": c.Gen, "fw_id": "20241011-114455/" + c.FW + "-g8ab4e4a",
			"ver": c.FW, "app": c.App, "auth_en": c.Password != "", "auth_domain": dom}
	}
	return map[string]any{"type": c.Model, "mac": c.MAC, "auth": c.Password != "", "fw": c.FW, "discoverable": true,
		"longid": 1, "num_outputs": len(c.Relays) + len(c.Lights), "num_meters": len(c.Relays), "num_rollers": len(c.Rollers)}
}

// ---- Gen1 ------------------------------------------------------------------

func (s *Sim) serveGen1(d *device, w http.ResponseWriter, r *http.Request) {
	if d.cfg.Password != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != "admin" || p != d.cfg.Password {
			if ok {
				s.mu.Lock()
				s.authFail++
				s.mu.Unlock()
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="`+d.cfg.MAC+`"`)
			http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	idx := 0
	if len(parts) == 2 {
		idx, _ = strconv.Atoi(parts[1])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &d.cfg
	s.calls["/"+parts[0]]++
	if parts[0] != "status" && parts[0] != "settings" {
		s.requests = append(s.requests, c.MAC+" "+r.URL.RequestURI())
	}
	switch parts[0] {
	case "cit":
		if len(parts) == 2 && parts[1] == "d" {
			writeJSON(w, 200, citDescription(c))
			return
		}
		http.NotFound(w, r)
	case "ota":
		s.mu.Unlock()
		ok := s.update(d, q.Get("url"))
		s.mu.Lock()
		if !ok {
			http.Error(w, "update refused", http.StatusBadRequest)
			return
		}
		writeJSON(w, 200, map[string]any{"status": "updating", "has_update": true})
	case "reboot":
		s.reboots++
		writeJSON(w, 200, map[string]any{"ok": true})
	case "status":
		writeJSON(w, 200, gen1Status(c))
	case "settings":
		if len(parts) >= 2 && (parts[1] == "actions" || len(parts) == 3) {
			writeJSON(w, 200, gen1SettingsCall(d, parts[1:], q))
			return
		}
		if len(parts) == 2 && parts[1] == "sta" {
			if en := q.Get("enabled"); en != "" {
				d.sta = STA{SSID: q.Get("ssid"), Password: q.Get("key"), Enabled: en == "1" || en == "true"}
			}
			writeJSON(w, 200, map[string]any{"enabled": d.sta.Enabled, "ssid": d.sta.SSID, "ipv4_method": "dhcp"})
			return
		}
		mode := c.Mode
		switch {
		case mode != "":
		case len(c.Lights) > 0 && c.Lights[0].Color:
			mode = "color" // a Bulb or RGBW2 in colour mode
		case len(c.Lights) > 0 && (c.Model == "SHRGBW2" || c.Model == "SHBLB-1"):
			mode = "white"
		default:
			mode = "relay"
		}
		writeJSON(w, 200, map[string]any{"device": map[string]any{"type": c.Model, "mac": c.MAC, "hostname": "shelly-" + c.MAC}, "name": c.Name, "mode": mode, "fw": c.FW})
	case "relay":
		if idx >= len(c.Relays) {
			http.Error(w, "Bad relay index", 400)
			return
		}
		was := c.Relays[idx].On
		switch q.Get("turn") {
		case "on":
			c.Relays[idx].On = true
		case "off":
			c.Relays[idx].On = false
		case "toggle":
			c.Relays[idx].On = !c.Relays[idx].On
		case "":
		default:
			http.Error(w, "Bad turn!", 400)
			return
		}
		if was != c.Relays[idx].On {
			s.action1Locked(d, map[bool]string{true: "out_on_url", false: "out_off_url"}[c.Relays[idx].On], idx, nil)
		}
		writeJSON(w, 200, map[string]any{"ison": c.Relays[idx].On, "has_timer": false})
	case "light":
		if idx >= len(c.Lights) {
			http.Error(w, "Bad light index", 400)
			return
		}
		l := &c.Lights[idx]
		switch q.Get("turn") {
		case "on":
			l.On = true
		case "off":
			l.On = false
		case "toggle":
			l.On = !l.On
		}
		if b := q.Get("brightness"); b != "" && !l.Color {
			n, err := strconv.Atoi(b)
			if err != nil || n < 0 || n > 100 {
				http.Error(w, "Bad brightness", 400)
				return
			}
			l.Brightness = n
		}
		writeJSON(w, 200, map[string]any{"ison": l.On, "brightness": l.Brightness})
	case "color":
		if idx >= len(c.Lights) || !c.Lights[idx].Color {
			http.Error(w, "Bad mode", 400)
			return
		}
		l := &c.Lights[idx]
		switch q.Get("turn") {
		case "on":
			l.On = true
		case "off":
			l.On = false
		case "toggle":
			l.On = !l.On
		}
		for k, p := range map[string]*int{"red": &l.Red, "green": &l.Green, "blue": &l.Blue, "gain": &l.Gain} {
			if v := q.Get(k); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n > 255 {
					http.Error(w, "Bad "+k, 400)
					return
				}
				*p = n
			}
		}
		writeJSON(w, 200, map[string]any{"ison": l.On, "mode": "color", "red": l.Red, "green": l.Green, "blue": l.Blue, "gain": l.Gain})
	case "roller":
		if idx >= len(c.Rollers) {
			http.Error(w, "Bad roller index", 400)
			return
		}
		ro := &c.Rollers[idx]
		switch q.Get("go") {
		case "open":
			ro.State, ro.Pos = "stop", 100
		case "close":
			ro.State, ro.Pos = "stop", 0
		case "stop":
			ro.State = "stop"
		case "to_pos":
			n, err := strconv.Atoi(q.Get("roller_pos"))
			if err != nil || n < 0 || n > 100 {
				http.Error(w, "Bad roller_pos", 400)
				return
			}
			ro.State, ro.Pos = "stop", n
		default:
			http.Error(w, "Bad go", 400)
			return
		}
		writeJSON(w, 200, map[string]any{"state": ro.State, "current_pos": ro.Pos})
	default:
		http.NotFound(w, r)
	}
}

func gen1Status(c *Device) map[string]any {
	st := map[string]any{"wifi_sta": map[string]any{"connected": true, "rssi": -61}, "mac": c.MAC, "unixtime": time.Now().Unix(), "has_update": false, "uptime": 1234}
	var relays, meters, lights, rollers []any
	for _, r := range c.Relays {
		relays = append(relays, map[string]any{"ison": r.On, "has_timer": false, "source": "http"})
		meters = append(meters, map[string]any{"power": r.Power, "is_valid": true, "total": r.TotalWm, "counters": []any{r.Power, 0, 0}})
	}
	for _, l := range c.Lights {
		if l.Color {
			lights = append(lights, map[string]any{"ison": l.On, "mode": "color", "red": l.Red, "green": l.Green, "blue": l.Blue, "gain": l.Gain})
		} else {
			lights = append(lights, map[string]any{"ison": l.On, "brightness": l.Brightness, "mode": "white"})
		}
		meters = append(meters, map[string]any{"power": 0.0, "is_valid": true, "total": 0})
	}
	for _, ro := range c.Rollers {
		rollers = append(rollers, map[string]any{"state": ro.State, "current_pos": ro.Pos, "positioning": true, "power": 0})
	}
	if relays != nil {
		st["relays"] = relays
	}
	if meters != nil {
		st["meters"] = meters
	}
	if lights != nil {
		st["lights"] = lights
	}
	if rollers != nil {
		st["rollers"] = rollers
	}
	if c.TempC != nil {
		st["tmp"] = map[string]any{"value": *c.TempC, "units": "C", "tC": *c.TempC, "is_valid": true}
	}
	if c.Hum != nil {
		st["hum"] = map[string]any{"value": *c.Hum, "is_valid": true}
	}
	st["temperature"] = 40.5 // the device's own temperature
	return st
}

// ---- Gen2 JSON-RPC -----------------------------------------------------------

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newNonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func parseDigest(h string) map[string]string {
	out := map[string]string{}
	h = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(h), "Digest"))
	for len(h) > 0 {
		h = strings.TrimLeft(h, " ,")
		eq := strings.IndexByte(h, '=')
		if eq < 0 {
			break
		}
		k := strings.TrimSpace(h[:eq])
		h = h[eq+1:]
		var v string
		if strings.HasPrefix(h, `"`) {
			end := strings.IndexByte(h[1:], '"')
			if end < 0 {
				break
			}
			v, h = h[1:end+1], h[end+2:]
		} else {
			end := strings.IndexByte(h, ',')
			if end < 0 {
				end = len(h)
			}
			v, h = strings.TrimSpace(h[:end]), h[end:]
		}
		out[k] = v
	}
	return out
}

func (s *Sim) checkHTTPDigest(d *device, r *http.Request) bool {
	a := r.Header.Get("Authorization")
	if a == "" {
		return false
	}
	p := parseDigest(a)
	s.mu.Lock()
	known := d.nonces[p["nonce"]]
	s.mu.Unlock()
	ha1 := sha("admin:" + d.cfg.ID + ":" + d.cfg.Password)
	ha2 := sha(r.Method + ":" + p["uri"])
	want := sha(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	ok := known && p["username"] == "admin" && p["realm"] == d.cfg.ID && strings.EqualFold(p["algorithm"], "SHA-256") && p["qop"] == "auth" && p["response"] == want
	if !ok {
		s.mu.Lock()
		s.authFail++
		s.mu.Unlock()
	}
	return ok
}

func (s *Sim) serveRPC(d *device, w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]any{"id": nil, "src": d.cfg.ID, "error": map[string]any{"code": -32700, "message": "parse error"}})
			return
		}
	}
	if d.cfg.Password != "" && req.Method != "Shelly.GetDeviceInfo" && !s.checkHTTPDigest(d, r) {
		n := newNonce()
		s.mu.Lock()
		d.nonces[n] = true
		s.mu.Unlock()
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest qop="auth", realm="%s", nonce="%s", algorithm=SHA-256`, d.cfg.ID, n))
		writeJSON(w, 401, map[string]any{"id": req.ID, "src": d.cfg.ID, "error": map[string]any{"code": 401, "message": "Unauthorized"}})
		return
	}
	res, rpcErr := s.call(d, req.Method, req.Params)
	if rpcErr != nil {
		writeJSON(w, 200, map[string]any{"id": req.ID, "src": d.cfg.ID, "error": rpcErr})
		return
	}
	writeJSON(w, 200, map[string]any{"id": req.ID, "src": d.cfg.ID, "result": res})
}

func rpcError(code int, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

func pnum(p map[string]any, k string) (float64, bool) {
	v, ok := p[k].(float64)
	return v, ok
}

// call runs an RPC method; the changed component is notified.
func (s *Sim) call(d *device, method string, p map[string]any) (any, map[string]any) {
	if method != "Shelly.GetStatus" && method != "Shelly.GetDeviceInfo" {
		b, _ := json.Marshal(p)
		s.count(method, d.cfg.ID+" "+method+" "+string(b))
	} else {
		s.count(method, "")
	}
	if p == nil {
		p = map[string]any{}
	}
	id, _ := pnum(p, "id")
	comp := func(typ string) (string, map[string]any, map[string]any) {
		key := typ + ":" + strconv.Itoa(int(id))
		s.mu.Lock()
		c := d.cfg.Components[key]
		s.mu.Unlock()
		if c == nil {
			return key, nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(id))+" not found!")
		}
		return key, c, nil
	}
	set := func(key string, fields map[string]any) {
		s.mu.Lock()
		c := d.cfg.Components[key]
		before := map[string]any{}
		for k, v := range c {
			before[k] = v
		}
		for k, v := range fields {
			c[k] = v
		}
		after := map[string]any{}
		for k, v := range c {
			after[k] = v
		}
		s.mu.Unlock()
		delta := map[string]any{"id": id}
		for k, v := range fields {
			delta[k] = v
		}
		go func() {
			s.notify(d, "NotifyStatus", map[string]any{key: delta})
			s.changed(d, key, before, after)
		}()
	}
	switch method {
	case "Shelly.GetDeviceInfo":
		return s.shellyInfo(d), nil
	case "Shelly.Update":
		// By URL, or from the vendor's stable or beta channel (stage), which
		// here installs what SetUpdate offers.
		u, _ := p["url"].(string)
		if st, _ := p["stage"].(string); u == "" && (st == "stable" || st == "beta") {
			u = "stage:" + st
		}
		if !s.update(d, u) {
			return nil, rpcError(-114, "Update refused")
		}
		return nil, nil
	case "Shelly.GetStatus":
		s.mu.Lock()
		defer s.mu.Unlock()
		b, _ := json.Marshal(d.cfg.Components)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out, nil
	case "Shelly.GetConfig":
		return s.config(d), nil
	case "Shelly.GetComponents":
		// Every component, or only the dynamic (virtual) ones, in pages
		// of 20 from offset.
		s.mu.Lock()
		defer s.mu.Unlock()
		dyn, _ := p["dynamic_only"].(bool)
		cfg := s.configLocked(d)
		x := d.ext()
		keys := make([]string, 0, len(d.cfg.Components))
		for key := range d.cfg.Components {
			if _, virtual := x.virtual[key]; dyn && !virtual {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		off := 0
		if o, ok := pnum(p, "offset"); ok {
			off = min(int(o), len(keys))
		}
		list := []any{}
		for _, key := range keys[off:min(off+20, len(keys))] {
			b, _ := json.Marshal(d.cfg.Components[key])
			var status map[string]any
			_ = json.Unmarshal(b, &status)
			list = append(list, map[string]any{"key": key, "status": status, "config": cfg[key]})
		}
		return map[string]any{"components": list, "cfg_rev": x.cfgRev, "offset": off, "total": len(keys)}, nil
	case "Switch.Set", "Switch.Toggle", "Light.Set", "Light.Toggle", "RGB.Set", "RGB.Toggle", "RGBW.Set", "RGBW.Toggle", "CCT.Set", "CCT.Toggle":
		typ := strings.ToLower(method[:strings.Index(method, ".")])
		key, c, e := comp(typ)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		was, _ := c["output"].(bool)
		s.mu.Unlock()
		on := !was
		if strings.HasSuffix(method, ".Set") {
			v, ok := p["on"].(bool)
			if !ok && p["brightness"] == nil && p["rgb"] == nil && p["white"] == nil && p["ct"] == nil {
				return nil, rpcError(-103, "Missing required argument 'on'!")
			}
			on = v || (!ok && was)
			if !ok && p["brightness"] != nil {
				on = true
			}
		}
		fields := map[string]any{"output": on}
		if b, ok := pnum(p, "brightness"); ok {
			if b < 0 || b > 100 {
				return nil, rpcError(-103, "Invalid argument 'brightness'")
			}
			fields["brightness"] = b
		}
		if v, has := p["rgb"]; has {
			l, ok := v.([]any)
			if !ok || len(l) != 3 {
				return nil, rpcError(-103, "Invalid argument 'rgb'")
			}
			for _, x := range l {
				if f, ok := x.(float64); !ok || f < 0 || f > 255 {
					return nil, rpcError(-103, "Invalid argument 'rgb'")
				}
			}
			fields["rgb"] = l
		}
		if w, ok := pnum(p, "white"); ok {
			if w < 0 || w > 255 {
				return nil, rpcError(-103, "Invalid argument 'white'")
			}
			fields["white"] = w
		}
		if ct, ok := pnum(p, "ct"); ok {
			if ct < 2700 || ct > 6500 {
				return nil, rpcError(-103, "Invalid argument 'ct'")
			}
			fields["ct"] = ct
		}
		set(key, fields)
		return map[string]any{"was_on": was}, nil
	case "Ws.GetConfig":
		s.mu.Lock()
		defer s.mu.Unlock()
		if d.cfg.Ws == nil {
			return nil, rpcError(-114, "Method Ws.GetConfig failed: No handler!")
		}
		return d.cfg.Ws, nil
	case "Ws.SetConfig":
		cfg, _ := p["config"].(map[string]any)
		s.mu.Lock()
		defer s.mu.Unlock()
		if d.cfg.Ws == nil || cfg == nil {
			return nil, rpcError(-103, "Missing required argument 'config'!")
		}
		for k, v := range cfg {
			d.cfg.Ws[k] = v
		}
		return map[string]any{"restart_required": true}, nil
	case "Shelly.Reboot":
		s.mu.Lock()
		s.reboots++
		s.mu.Unlock()
		return nil, nil
	case "WiFi.SetConfig":
		cfg, _ := p["config"].(map[string]any)
		sta, _ := cfg["sta"].(map[string]any)
		if sta == nil {
			return nil, rpcError(-103, "Missing required argument 'config'!")
		}
		ssid, _ := sta["ssid"].(string)
		pass, _ := sta["pass"].(string)
		en, _ := sta["enable"].(bool)
		s.mu.Lock()
		d.sta = STA{SSID: ssid, Password: pass, Enabled: en}
		s.mu.Unlock()
		return map[string]any{"restart_required": false}, nil
	case "Cover.Open", "Cover.Close", "Cover.Stop", "Cover.GoToPosition":
		key, _, e := comp("cover")
		if e != nil {
			return nil, e
		}
		// set's notifications run in the background; a timed move's steps
		// call it from their own goroutine.
		syncSet := func(k string, fields map[string]any) {
			set(k, fields)
		}
		switch method {
		case "Cover.Open":
			s.moveCover(d, key, id, 100, syncSet)
		case "Cover.Close":
			s.moveCover(d, key, id, 0, syncSet)
		case "Cover.Stop":
			s.stopCover(d, key)
			set(key, map[string]any{"state": "stopped", "target_pos": nil})
		default:
			pos, ok := pnum(p, "pos")
			if !ok || pos < 0 || pos > 100 {
				return nil, rpcError(-103, "Invalid argument 'pos'")
			}
			s.moveCover(d, key, id, pos, syncSet)
		}
		return nil, nil
	}
	if res, e, ok := s.callMore(d, method, p); ok {
		return res, e
	}
	// <Component>.GetStatus: one component's status ("Cover.GetStatus"
	// with an id, "Sys.GetStatus" without).
	if typ, ok := strings.CutSuffix(method, ".GetStatus"); ok {
		key := strings.ToLower(typ)
		if _, has := p["id"]; has {
			key += ":" + strconv.Itoa(int(id))
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		st, found := d.cfg.Components[key]
		if found {
			b, _ := json.Marshal(st)
			var out map[string]any
			_ = json.Unmarshal(b, &out)
			return out, nil
		}
		if strings.Contains(key, ":") {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(id))+" not found!")
		}
	}
	return nil, rpcError(-114, "Method "+method+" failed: No handler!")
}

// config is a Gen2+ device's Shelly.GetConfig: a configuration object per
// component (its id and name), the device's name under sys and the
// outbound WebSocket's settings under ws.
func (s *Sim) config(d *device) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configLocked(d)
}

// ---- Gen2 WebSocket ------------------------------------------------------------

func (s *Sim) serveWS(d *device, w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		s.mu.Lock()
		delete(d.clients, c)
		s.mu.Unlock()
		_ = c.CloseNow()
	}()
	ctx := r.Context()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID     any             `json:"id"`
			Src    string          `json:"src"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
			Auth   json.RawMessage `json:"auth"`
		}
		if json.Unmarshal(b, &req) != nil || req.Src == "" {
			continue
		}
		reply := map[string]any{"id": req.ID, "src": d.cfg.ID, "dst": req.Src}
		d.wsMu.Lock()
		// Shelly.GetDeviceInfo needs no authentication, over WebSocket as
		// over HTTP (clients read the realm from it before they log in); a
		// channel gets notifications only once it has authenticated.
		authed := d.cfg.Password == "" || s.checkWSAuth(d, req.Auth)
		if !authed && req.Method == "Shelly.GetDeviceInfo" {
			reply["result"] = s.shellyInfo(d)
			s.count(req.Method, "")
		} else if !authed {
			s.mu.Lock()
			d.wsNonce = time.Now().Unix() + int64(len(d.clients))
			n := d.wsNonce
			s.mu.Unlock()
			ch, _ := json.Marshal(map[string]any{"auth_type": "digest", "nonce": n, "nc": 1, "realm": d.cfg.ID, "algorithm": "SHA-256"})
			reply["error"] = rpcError(401, string(ch))
		} else {
			s.mu.Lock()
			d.clients[c] = req.Src
			s.mu.Unlock()
			res, e := s.call(d, req.Method, req.Params)
			if e != nil {
				reply["error"] = e
			} else {
				reply["result"] = res
			}
		}
		out, _ := json.Marshal(reply)
		wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = c.Write(wctx, websocket.MessageText, out)
		cancel()
		d.wsMu.Unlock()
		if err != nil {
			return
		}
	}
}

// jsonText gives a JSON number or string as the text the digest uses.
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return new(big.Float).SetFloat64(x).Text('f', -1)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func (s *Sim) checkWSAuth(d *device, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var a map[string]any
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	s.mu.Lock()
	nonce := d.wsNonce
	s.mu.Unlock()
	ha1 := sha("admin:" + d.cfg.ID + ":" + d.cfg.Password)
	ha2 := sha("dummy_method:dummy_uri")
	want := sha(ha1 + ":" + jsonText(a["nonce"]) + ":" + jsonText(a["nc"]) + ":" + jsonText(a["cnonce"]) + ":auth:" + ha2)
	ok := nonce != 0 && jsonText(a["nonce"]) == strconv.FormatInt(nonce, 10) && a["username"] == "admin" && a["realm"] == d.cfg.ID &&
		a["algorithm"] == "SHA-256" && a["response"] == want
	if !ok {
		s.mu.Lock()
		s.authFail++
		s.mu.Unlock()
	}
	return ok
}

// ---- Gen2 BLE RPC ------------------------------------------------------------

// Shelly BLE RPC GATT characteristics.
const (
	bleData = "5f6d4f53-5f52-5043-5f64-6174615f5f5f"
	bleTx   = "5f6d4f53-5f52-5043-5f74-785f63746c5f"
	bleRx   = "5f6d4f53-5f52-5043-5f72-785f63746c5f"
)

// BLEPeer is a device's Bluetooth RPC service as a GATT peer: a request's
// length is written to TX control (4 octets, big-endian), the request to
// data; the answer's length is read from RX control and the answer from
// data in chunks of at most 20 octets.
type BLEPeer struct {
	s      *Sim
	d      *device
	mu     sync.Mutex
	want   int
	req    []byte
	resp   []byte
	Writes int // characteristic writes, for tests
}

// BLE returns a device's Bluetooth peer, nil for a Gen1 device.
func (s *Sim) BLE(key string) *BLEPeer {
	d := s.find(key)
	if d == nil || d.cfg.Gen < 2 {
		return nil
	}
	return &BLEPeer{s: s, d: d}
}

// Name is the device's advertised name.
func (p *BLEPeer) Name() string { return p.d.cfg.App + "-" + p.d.cfg.MAC }

// Write writes a characteristic.
func (p *BLEPeer) Write(_ context.Context, char string, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Writes++
	switch char {
	case bleTx:
		if len(b) != 4 {
			return fmt.Errorf("tx control takes 4 octets, not %d", len(b))
		}
		p.want = int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		p.req, p.resp = nil, nil
	case bleData:
		if len(b) > 20 {
			return fmt.Errorf("a write of %d octets exceeds the ATT payload", len(b))
		}
		p.req = append(p.req, b...)
		if p.want > 0 && len(p.req) == p.want {
			var r struct {
				ID     any            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			out := map[string]any{"src": p.d.cfg.ID}
			if err := json.Unmarshal(p.req, &r); err != nil {
				out["error"] = rpcError(-32700, "parse error")
			} else {
				out["id"] = r.ID
				res, e := p.s.call(p.d, r.Method, r.Params)
				if e != nil {
					out["error"] = e
				} else {
					out["result"] = res
				}
			}
			p.resp, _ = json.Marshal(out)
			p.want = 0
		}
	default:
		return fmt.Errorf("no characteristic %s", char)
	}
	return nil
}

// Read reads a characteristic.
func (p *BLEPeer) Read(_ context.Context, char string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch char {
	case bleRx:
		n := len(p.resp)
		return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, nil
	case bleData:
		n := min(20, len(p.resp))
		out := p.resp[:n]
		p.resp = p.resp[n:]
		return out, nil
	}
	return nil, fmt.Errorf("no characteristic %s", char)
}
