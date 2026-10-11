package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The push paths of Shelly devices, as Shelly's documents describe them:
// Gen1 CoIoT (a CoAP message to /cit/s with the state as [channel, sensor
// ID, value] triples, the IDs described at GET /cit/d), Gen1 MQTT topics
// under shellies/<id>/, Gen2+ JSON-RPC over MQTT and the Gen2+ outbound
// WebSocket a sleeping device opens to a server.

// Sensor IDs of the simulated description (relay_n, light_n and input_n
// blocks are numbered from 1 in that order; block 9 is the device).
func sid(kind, n int) int { return 1000*kind + 100*(n+1) + 1 }

// citDescription is a Gen1 device's /cit/d.
func citDescription(c *Device) map[string]any {
	var blk, sen []any
	b := 1
	for i := range c.Relays {
		blk = append(blk, map[string]any{"I": b, "D": "relay_" + strconv.Itoa(i)})
		sen = append(sen, map[string]any{"I": sid(1, i), "T": "S", "D": "output", "R": "0/1", "L": b},
			map[string]any{"I": sid(4, i), "T": "P", "D": "power", "U": "W", "R": "0/3500", "L": b},
			map[string]any{"I": sid(4, i) + 2, "T": "E", "D": "energy", "U": "Wmin", "R": "U32", "L": b})
		b++
	}
	for i, l := range c.Lights {
		blk = append(blk, map[string]any{"I": b, "D": "light_" + strconv.Itoa(i)})
		sen = append(sen, map[string]any{"I": sid(5, i), "T": "S", "D": "output", "R": "0/1", "L": b})
		if l.Color {
			for k, d := range []string{"red", "green", "blue"} {
				sen = append(sen, map[string]any{"I": sid(5, i) + 2 + k, "T": "S", "D": d, "R": "0/255", "L": b})
			}
			sen = append(sen, map[string]any{"I": sid(5, i) + 5, "T": "S", "D": "gain", "R": "0/100", "L": b})
		} else {
			sen = append(sen, map[string]any{"I": sid(5, i) + 1, "T": "S", "D": "brightness", "R": "0/100", "L": b})
		}
		b++
	}
	for i := range c.Inputs {
		blk = append(blk, map[string]any{"I": b, "D": "input_" + strconv.Itoa(i)})
		sen = append(sen, map[string]any{"I": sid(2, i), "T": "S", "D": "input", "R": "0/1", "L": b},
			map[string]any{"I": sid(2, i) + 1, "T": "EV", "D": "inputEvent", "R": []any{"S/L", ""}, "L": b},
			map[string]any{"I": sid(2, i) + 2, "T": "EVC", "D": "inputEventCnt", "R": "U16", "L": b})
		b++
	}
	blk = append(blk, map[string]any{"I": 9, "D": "device"})
	if c.TempC != nil {
		sen = append(sen, map[string]any{"I": 3101, "T": "T", "D": "temperature", "U": "C", "R": []any{"-55/125", "-999"}, "L": 9},
			map[string]any{"I": 3102, "T": "T", "D": "temperature", "U": "F", "R": []any{"-67/257", "-999"}, "L": 9})
	}
	if c.Hum != nil {
		sen = append(sen, map[string]any{"I": 3103, "T": "H", "D": "humidity", "U": "%", "R": []any{"0/100", "-999"}, "L": 9})
	}
	if c.Battery != nil {
		sen = append(sen, map[string]any{"I": 3111, "T": "B", "D": "battery", "U": "%", "R": []any{"0/100", "-1"}, "L": 9})
	}
	sen = append(sen, map[string]any{"I": 9103, "T": "EVC", "D": "cfgChanged", "R": "U16", "L": 9})
	return map[string]any{"blk": blk, "sen": sen}
}

// coiotPayload is a Gen1 device's /cit/s payload.
func coiotPayload(c *Device) []byte {
	var g []any
	for i, r := range c.Relays {
		on := 0
		if r.On {
			on = 1
		}
		g = append(g, []any{0, sid(1, i), on}, []any{0, sid(4, i), r.Power}, []any{0, sid(4, i) + 2, r.TotalWm})
	}
	for i, l := range c.Lights {
		on := 0
		if l.On {
			on = 1
		}
		g = append(g, []any{0, sid(5, i), on})
		if l.Color {
			g = append(g, []any{0, sid(5, i) + 2, l.Red}, []any{0, sid(5, i) + 3, l.Green}, []any{0, sid(5, i) + 4, l.Blue}, []any{0, sid(5, i) + 5, l.Gain})
		} else {
			g = append(g, []any{0, sid(5, i) + 1, l.Brightness})
		}
	}
	for i, in := range c.Inputs {
		g = append(g, []any{0, sid(2, i), 0}, []any{0, sid(2, i) + 1, in.Event}, []any{0, sid(2, i) + 2, in.Cnt})
	}
	if c.TempC != nil {
		g = append(g, []any{0, 3101, *c.TempC}, []any{0, 3102, *c.TempC*9/5 + 32})
	}
	if c.Hum != nil {
		g = append(g, []any{0, 3103, *c.Hum})
	}
	if c.Battery != nil {
		g = append(g, []any{0, 3111, *c.Battery})
	}
	g = append(g, []any{0, 9103, 0})
	b, _ := json.Marshal(map[string]any{"G": g})
	return b
}

// SendCoIoT sends the device's CoIoT status message to addr (the
// listener under test; real devices multicast it). The serial changes
// with the state.
func (s *Sim) SendCoIoT(key, addr string) error {
	d := s.find(key)
	s.mu.Lock()
	payload := coiotPayload(&d.cfg)
	if s.serials == nil {
		s.serials = map[string]int{}
	}
	sig := string(payload)
	serial := s.serials[sig]
	if serial == 0 {
		serial = len(s.serials) + 1
		s.serials[sig] = serial
	}
	msg := coapStatus(d.cfg.Model, d.cfg.MAC, serial, payload)
	s.mu.Unlock()
	c, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(msg)
	return err
}

// Press1 presses a Gen1 input (event S, L, SS …).
func (s *Sim) Press1(key string, input int, event string) {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	in := &d.cfg.Inputs[input]
	in.Event = event
	in.Cnt++
}

// Gen1Topics are the MQTT messages a Gen1 device publishes for its state
// (topic under shellies/<id>/ → payload).
func (s *Sim) Gen1Topics(key string) map[string]string {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &d.cfg
	out := map[string]string{"online": "true"}
	for i, r := range c.Relays {
		n := strconv.Itoa(i)
		out["relay/"+n] = map[bool]string{true: "on", false: "off"}[r.On]
		out["relay/"+n+"/power"] = strconv.FormatFloat(r.Power, 'f', 2, 64)
		out["relay/"+n+"/energy"] = strconv.FormatFloat(r.TotalWm, 'f', 0, 64)
	}
	for i, in := range c.Inputs {
		b, _ := json.Marshal(map[string]any{"event": in.Event, "event_cnt": in.Cnt})
		out["input_event/"+strconv.Itoa(i)] = string(b)
	}
	if c.TempC != nil {
		out["sensor/temperature"] = strconv.FormatFloat(*c.TempC, 'f', 2, 64)
	}
	if c.Hum != nil {
		out["sensor/humidity"] = strconv.FormatFloat(*c.Hum, 'f', 1, 64)
	}
	if c.Battery != nil {
		out["sensor/battery"] = strconv.FormatFloat(*c.Battery, 'f', 0, 64)
	}
	return out
}

// MQTTCommand applies a Gen1 MQTT command (topic under shellies/<id>/)
// as the device does; false when the device does not take it.
func (s *Sim) MQTTCommand(key, topic, payload string) bool {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &d.cfg
	p := strings.Split(topic, "/")
	if len(p) == 3 && p[0] == "relay" && p[2] == "command" {
		i, err := strconv.Atoi(p[1])
		if err != nil || i >= len(c.Relays) {
			return false
		}
		switch payload {
		case "on":
			c.Relays[i].On = true
		case "off":
			c.Relays[i].On = false
		case "toggle":
			c.Relays[i].On = !c.Relays[i].On
		default:
			return false
		}
		s.requests = append(s.requests, c.MAC+" mqtt "+topic+" "+payload)
		return true
	}
	return false
}

// RPC runs a Gen2+ method as the device does (for its MQTT RPC); it
// returns the result or the error object.
func (s *Sim) RPC(key, method string, params map[string]any) (any, map[string]any) {
	return s.call(s.find(key), method, params)
}

// Reboots counts Shelly.Reboot calls and Gen1 /reboot requests.
func (s *Sim) Reboots() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reboots
}

// WsConfig is a Gen2+ device's outbound WebSocket configuration.
func (s *Sim) WsConfig(key string) map[string]any {
	d := s.find(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for k, v := range d.cfg.Ws {
		out[k] = v
	}
	return out
}

// DialOutbound opens the device's outbound WebSocket to url, as a
// sleeping Gen2+ device does when it wakes: NotifyFullStatus first, then
// the notifications of its changes while the channel is open. It returns
// a function that closes the channel (the device sleeps again).
func (s *Sim) DialOutbound(key, url string) (func(), error) {
	d := s.find(key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	c, _, err := websocket.Dial(ctx, url, nil)
	cancel()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if d.clients == nil {
		d.clients = map[*websocket.Conn]string{}
	}
	d.clients[c] = "ws"
	src := d.key()
	b, _ := json.Marshal(d.cfg.Components)
	s.mu.Unlock()
	var full map[string]any
	_ = json.Unmarshal(b, &full)
	full["ts"] = float64(time.Now().UnixMilli()) / 1000
	msg, _ := json.Marshal(map[string]any{"src": src, "dst": "ws", "method": "NotifyFullStatus", "params": full})
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	if err := c.Write(wctx, websocket.MessageText, msg); err != nil {
		_ = c.CloseNow()
		return nil, fmt.Errorf("outbound WebSocket: %w", err)
	}
	return func() {
		s.mu.Lock()
		delete(d.clients, c)
		s.mu.Unlock()
		_ = c.Close(websocket.StatusNormalClosure, "sleep")
	}, nil
}

// coapStatus builds a CoIoT status message: CoAP version 1,
// non-confirmable, code 0.30, a message ID, the options Uri-Path "cit",
// "s", 3332 (type#id#2), 3412 (validity) and 3420 (serial) in ascending
// order, each with its delta and length nibbles (extended by one byte at
// 13, two at 14), then 0xFF and the payload.
func coapStatus(devType, devID string, serial int, payload []byte) []byte {
	out := []byte{0x50, 0x1e, 0x12, 0x34}
	prev := 0
	opt := func(n int, v []byte) {
		d, l := n-prev, len(v)
		prev = n
		var hd, hl byte
		var ext []byte
		switch {
		case d >= 269:
			hd = 14
			ext = append(ext, byte((d-269)>>8), byte(d-269))
		case d >= 13:
			hd = 13
			ext = append(ext, byte(d-13))
		default:
			hd = byte(d)
		}
		var extl []byte
		if l >= 13 {
			hl = 13
			extl = []byte{byte(l - 13)}
		} else {
			hl = byte(l)
		}
		out = append(out, hd<<4|hl)
		out = append(out, ext...)
		out = append(out, extl...)
		out = append(out, v...)
	}
	opt(11, []byte("cit"))
	opt(11, []byte("s"))
	opt(3332, []byte(devType+"#"+devID+"#2"))
	opt(3412, []byte{0x00, 0x26})
	opt(3420, []byte{byte(serial >> 8), byte(serial)})
	return append(append(out, 0xff), payload...)
}
