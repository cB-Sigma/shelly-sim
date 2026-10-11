// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// A device in MQTT mode: it connects to a broker as a client and speaks
// the topics real devices use. A small MQTT 3.1.1 client (QoS 0 out,
// QoS 0 and 1 in) is written here so the module needs no MQTT library.

// MQTTOptions configure ConnectMQTT.
type MQTTOptions struct {
	// User and Password log in to the broker when set.
	User, Password string
	// Prefix is a Gen1 device's topic root (shellies when empty).
	Prefix string
	// StatusNotify makes a Gen2+ device also publish each changed
	// component's whole status on <id>/status/<component>, as its
	// status_ntf setting does.
	StatusNotify bool
	// KeepAlive is the keep-alive sent to the broker (30 s when 0).
	KeepAlive time.Duration
}

// Gen1MQTTID is a Gen1 device's MQTT id: shelly<kind>-<MAC>.
func Gen1MQTTID(model, mac string) string { return "shelly" + gen1Kind(model) + "-" + mac }

// ConnectMQTT connects device key to the MQTT broker at addr (host:port)
// as a device in MQTT mode does. A Gen2+ device announces <id>/online,
// answers JSON-RPC requests on <id>/rpc to <src>/rpc and publishes its
// notifications on <id>/events/rpc. A Gen1 device publishes
// <prefix>/announce and its state topics under <prefix>/<id>/, takes
// relay commands on <prefix>/<id>/relay/<n>/command (and republishes
// its state) and answers <prefix>/command "announce". The returned
// function disconnects.
func (s *Sim) ConnectMQTT(key, addr string, o MQTTOptions) (func(), error) {
	d := s.find(key)
	if d == nil {
		return nil, fmt.Errorf("no device %s", key)
	}
	s.mu.Lock()
	c := d.cfg
	s.mu.Unlock()
	prefix := o.Prefix
	if prefix == "" {
		prefix = "shellies"
	}
	id, will := c.ID, c.ID+"/online"
	if c.Gen < 2 {
		id = Gen1MQTTID(c.Model, c.MAC)
		will = prefix + "/" + id + "/online"
	}
	ka := o.KeepAlive
	if ka <= 0 {
		ka = 30 * time.Second
	}
	m, err := dialMQTT(addr, id, o.User, o.Password, will, ka)
	if err != nil {
		return nil, err
	}
	var undo func()
	if c.Gen >= 2 {
		undo = s.mqttGen2(d, m, id, o)
	} else {
		undo = s.mqttGen1(d, m, id, prefix)
	}
	return func() {
		undo()
		m.close()
	}, nil
}

func (s *Sim) mqttGen2(d *device, m *mqttConn, id string, o MQTTOptions) func() {
	_ = m.publish(id+"/online", []byte("true"), true)
	m.setHandler(func(topic string, payload []byte) {
		if topic != id+"/rpc" {
			return
		}
		var req struct {
			ID     any            `json:"id"`
			Src    string         `json:"src"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(payload, &req) != nil || req.Src == "" {
			return
		}
		res, e := s.call(d, req.Method, req.Params)
		out := map[string]any{"id": req.ID, "src": id, "dst": req.Src}
		if e != nil {
			out["error"] = e
		} else {
			out["result"] = res
		}
		b, _ := json.Marshal(out)
		_ = m.publish(req.Src+"/rpc", b, false)
	})
	_ = m.subscribe(id + "/rpc")
	return s.addSink(d, func(method string, params map[string]any) {
		b, _ := json.Marshal(map[string]any{"src": id, "dst": id + "/events", "method": method, "params": params})
		_ = m.publish(id+"/events/rpc", b, false)
		if !o.StatusNotify || method != "NotifyStatus" {
			return
		}
		for comp := range params {
			if comp == "ts" {
				continue
			}
			st, _ := json.Marshal(s.Component(d.key(), comp))
			_ = m.publish(id+"/status/"+comp, st, false)
		}
	})
}

func (s *Sim) mqttGen1(d *device, m *mqttConn, id, prefix string) func() {
	key := d.key()
	publishState := func() {
		for t, v := range s.Gen1Topics(key) {
			_ = m.publish(prefix+"/"+id+"/"+t, []byte(v), t == "online")
		}
	}
	announce := func() {
		s.mu.Lock()
		c := d.cfg
		s.mu.Unlock()
		host, _, _ := net.SplitHostPort(strings.TrimPrefix(d.srv.URL, "http://"))
		b, _ := json.Marshal(map[string]any{"id": id, "model": c.Model, "mac": c.MAC, "ip": host, "new_fw": false, "fw_ver": c.FW})
		_ = m.publish(prefix+"/announce", b, false)
	}
	m.setHandler(func(topic string, payload []byte) {
		if topic == prefix+"/command" && string(payload) == "announce" {
			announce()
			return
		}
		rest, ok := strings.CutPrefix(topic, prefix+"/"+id+"/")
		if !ok {
			return
		}
		if s.MQTTCommand(key, rest, string(payload)) {
			publishState()
		}
	})
	_ = m.subscribe(prefix + "/" + id + "/+/+/command")
	_ = m.subscribe(prefix + "/command")
	announce()
	publishState()
	return func() {}
}

// ---- MQTT 3.1.1 client ----------------------------------------------------------

type mqttConn struct {
	c         net.Conn
	wmu       sync.Mutex
	nextID    uint16
	hmu       sync.Mutex
	onMessage func(topic string, payload []byte)
	done      chan struct{}
	once      sync.Once
}

func mqttString(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func mqttPacket(header byte, body []byte) []byte {
	out := []byte{header}
	n := len(body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

func dialMQTT(addr, clientID, user, pass, will string, keepAlive time.Duration) (*mqttConn, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	flags := byte(0x02)  // clean session
	flags |= 0x04 | 0x20 // a retained will at QoS 0
	ka := uint16(keepAlive / time.Second)
	body := append(mqttString("MQTT"), 4, flags, byte(ka>>8), byte(ka))
	body = append(body, mqttString(clientID)...)
	body = append(body, mqttString(will)...)
	body = append(body, mqttString("false")...)
	if user != "" {
		body[7] |= 0x80
		body = append(body, mqttString(user)...)
		if pass != "" {
			body[7] |= 0x40
			body = append(body, mqttString(pass)...)
		}
	}
	m := &mqttConn{c: c, done: make(chan struct{})}
	if _, err := c.Write(mqttPacket(0x10, body)); err != nil {
		c.Close()
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	typ, pkt, err := readPacket(r)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("MQTT CONNACK: %w", err)
	}
	if typ>>4 != 2 || len(pkt) < 2 || pkt[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("the broker refused the connection (%x)", pkt)
	}
	_ = c.SetReadDeadline(time.Time{})
	go m.read(r)
	go m.ping(keepAlive)
	return m, nil
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n, mul := 0, 1
	for i := 0; ; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		n += int(b&0x7f) * mul
		mul *= 128
		if b&0x80 == 0 {
			break
		}
		if i == 3 {
			return 0, nil, errors.New("bad remaining length")
		}
	}
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return h, buf, err
}

func (m *mqttConn) write(b []byte) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	_ = m.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := m.c.Write(b)
	return err
}

func (m *mqttConn) publish(topic string, payload []byte, retain bool) error {
	h := byte(0x30)
	if retain {
		h |= 0x01
	}
	return m.write(mqttPacket(h, append(mqttString(topic), payload...)))
}

func (m *mqttConn) subscribe(filter string) error {
	m.wmu.Lock()
	m.nextID++
	id := m.nextID
	m.wmu.Unlock()
	body := []byte{byte(id >> 8), byte(id)}
	body = append(body, mqttString(filter)...)
	body = append(body, 1) // QoS 1
	return m.write(mqttPacket(0x82, body))
}

func (m *mqttConn) read(r *bufio.Reader) {
	defer m.close()
	for {
		h, pkt, err := readPacket(r)
		if err != nil {
			return
		}
		if h>>4 != 3 || len(pkt) < 2 {
			continue // SUBACK, PINGRESP
		}
		qos := (h >> 1) & 3
		tl := int(binary.BigEndian.Uint16(pkt))
		if len(pkt) < 2+tl {
			continue
		}
		topic := string(pkt[2 : 2+tl])
		rest := pkt[2+tl:]
		if qos > 0 {
			if len(rest) < 2 {
				continue
			}
			_ = m.write(mqttPacket(0x40, rest[:2])) // PUBACK
			rest = rest[2:]
		}
		m.hmu.Lock()
		fn := m.onMessage
		m.hmu.Unlock()
		if fn != nil {
			payload := append([]byte(nil), rest...)
			go fn(topic, payload)
		}
	}
}

func (m *mqttConn) setHandler(fn func(topic string, payload []byte)) {
	m.hmu.Lock()
	m.onMessage = fn
	m.hmu.Unlock()
}

func (m *mqttConn) ping(every time.Duration) {
	t := time.NewTicker(every / 2)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
			if m.write([]byte{0xc0, 0}) != nil {
				return
			}
		}
	}
}

func (m *mqttConn) close() {
	m.once.Do(func() {
		close(m.done)
		_ = m.write([]byte{0xe0, 0}) // DISCONNECT: no will
		_ = m.c.Close()
	})
}
