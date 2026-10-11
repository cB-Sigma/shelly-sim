// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cB-Sigma/shelly/bthome"
)

// Bluetooth on simulated Gen2+ devices: the BTHome component (BTHome.*,
// BTHomeDevice and BTHomeSensor components, discovery events, button
// events and webhooks) and relay scripts. Hear feeds the device an
// advertisement, as its radio would. Decoding uses
// github.com/cB-Sigma/shelly/bthome, so the simulator and the library
// share the BTHome decoder; that decoder is checked against bthome-ble on
// its own (the library's interop tests).

// bluHeard is what a device remembers of a sender it heard.
type bluHeard struct {
	addr      string
	name      string
	rssi      int
	encrypted bool
	mf        map[string]any
	objects   map[[2]int]bool // object id, index
}

type bluState struct {
	keys  map[string][]byte // address → AES key of added devices
	heard map[string]*bluHeard
	guard bthome.Guard
	disc  map[string]any // discovery in progress
}

func (d *device) blu() *bluState {
	x := d.ext()
	if x.blu == nil {
		x.blu = &bluState{keys: map[string][]byte{}, heard: map[string]*bluHeard{}}
	}
	return x.blu
}

// bthomeDeviceEvents are the BTHomeDevice events, by the BTHome event
// name they come from.
var bthomeDeviceEvents = map[string]string{"press": "single_push", "double_press": "double_push", "triple_press": "triple_push",
	"long_press": "long_push", "long_double_press": "long_double_push", "long_triple_press": "long_triple_push",
	"rotate_left": "rotate_left", "rotate_right": "rotate_right"}

func normAddr(a string) string { return strings.ToLower(a) }

// bthomeCall answers BTHome.* and BTHomeDevice.GetKnownObjects.
func (s *Sim) bthomeCall(d *device, method string, p map[string]any) (any, map[string]any) {
	_, op, _ := strings.Cut(method, ".")
	if method == "BTHomeDevice.GetKnownObjects" {
		return s.knownObjects(d, p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x := d.ext()
	b := d.blu()
	freeID := func(typ string, p map[string]any) (int, map[string]any) {
		if f, ok := pnum(p, "id"); ok {
			id := int(f)
			if id < 200 || id > 299 {
				return 0, rpcError(-103, "Invalid argument 'id'!")
			}
			if _, used := d.cfg.Components[typ+":"+strconv.Itoa(id)]; used {
				return 0, rpcError(-103, "Component "+typ+":"+strconv.Itoa(id)+" already exists!")
			}
			return id, nil
		}
		for id := 200; id < 300; id++ {
			if _, used := d.cfg.Components[typ+":"+strconv.Itoa(id)]; !used {
				return id, nil
			}
		}
		return 0, rpcError(-109, "Too many components!")
	}
	switch op {
	case "GetConfig":
		return map[string]any{}, nil
	case "SetConfig", "ResetEncryptionCounter":
		return nil, nil
	case "GetStatus":
		st := map[string]any{}
		if b.disc != nil {
			st["discovery"] = clone(b.disc)
		}
		return st, nil
	case "AddDevice", "AddSensor":
		cfg, _ := p["config"].(map[string]any)
		addr, _ := cfg["addr"].(string)
		if cfg == nil || addr == "" {
			return nil, rpcError(-103, "Missing required argument 'config.addr'!")
		}
		typ := "bthomedevice"
		if op == "AddSensor" {
			typ = "bthomesensor"
			if s.bthomeDeviceKeyLocked(d, addr) == "" {
				return nil, rpcError(-105, "BTHomeDevice with address "+addr+" not found!")
			}
		}
		id, e := freeID(typ, p)
		if e != nil {
			return nil, e
		}
		key := typ + ":" + strconv.Itoa(id)
		conf := map[string]any{"id": float64(id), "name": cfg["name"], "addr": normAddr(addr), "meta": cfg["meta"]}
		st := map[string]any{"id": float64(id), "last_updated_ts": 0.0}
		if typ == "bthomedevice" {
			if k, ok := cfg["key"].(string); ok && k != "" {
				kb, err := hex.DecodeString(k)
				if err != nil || len(kb) != 16 {
					return nil, rpcError(-103, "Invalid argument 'config.key'!")
				}
				b.keys[normAddr(addr)] = kb
			}
			_, hasKey := b.keys[normAddr(addr)]
			for k, v := range map[string]any{"rssi": nil, "battery": nil, "packet_id": nil, "key": hasKey, "paired": false, "rpc": false, "rsv": -1.0, "fw_ver": ""} {
				st[k] = v
			}
		} else {
			oid, _ := pnum(cfg, "obj_id")
			idx, _ := pnum(cfg, "idx")
			conf["obj_id"], conf["idx"] = oid, idx
			st["value"] = nil
		}
		x.virtual[key] = conf
		d.cfg.Components[key] = st
		x.cfgRev++
		return map[string]any{"added": key}, nil
	case "DeleteDevice", "DeleteSensor":
		f, _ := pnum(p, "id")
		typ := map[string]string{"DeleteDevice": "bthomedevice", "DeleteSensor": "bthomesensor"}[op]
		key := typ + ":" + strconv.Itoa(int(f))
		conf, ok := x.virtual[key]
		if !ok {
			return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(f))+" not found!")
		}
		delete(x.virtual, key)
		delete(d.cfg.Components, key)
		if typ == "bthomedevice" {
			addr, _ := conf["addr"].(string)
			delete(b.keys, addr)
			for k, c := range x.virtual {
				if strings.HasPrefix(k, "bthomesensor:") && c["addr"] == addr {
					delete(x.virtual, k)
					delete(d.cfg.Components, k)
				}
			}
		}
		x.cfgRev++
		return nil, nil
	case "StartDeviceDiscovery":
		dur := 30.0
		if f, ok := pnum(p, "duration"); ok {
			dur = f
		}
		b.disc = map[string]any{"started_at": float64(time.Now().Unix()), "duration": dur}
		go s.discover(d)
		return nil, nil
	case "GetObjectInfos":
		ids, _ := p["obj_ids"].([]any)
		var objs []any
		for _, v := range ids {
			f, _ := v.(float64)
			info, ok := bthome.Info(byte(f))
			if !ok {
				continue
			}
			o := map[string]any{"obj_id": f, "obj_name": info.Name, "type": info.Kind.String()}
			if info.Unit != "" {
				o["unit"] = info.Unit
			}
			objs = append(objs, o)
		}
		return map[string]any{"objects": objs, "offset": 0.0, "count": float64(len(objs)), "total": float64(len(objs))}, nil
	}
	return nil, rpcError(-114, "Method "+method+" failed: No handler!")
}

// bthomeDeviceKeyLocked is the component key of the BTHomeDevice for addr.
func (s *Sim) bthomeDeviceKeyLocked(d *device, addr string) string {
	if d.x == nil {
		return ""
	}
	for k, c := range d.x.virtual {
		if strings.HasPrefix(k, "bthomedevice:") && c["addr"] == normAddr(addr) {
			return k
		}
	}
	return ""
}

func (s *Sim) knownObjects(d *device, p map[string]any) (any, map[string]any) {
	f, _ := pnum(p, "id")
	key := "bthomedevice:" + strconv.Itoa(int(f))
	s.mu.Lock()
	defer s.mu.Unlock()
	conf, ok := d.ext().virtual[key]
	if !ok {
		return nil, rpcError(-105, "Argument 'id', value "+strconv.Itoa(int(f))+" not found!")
	}
	addr, _ := conf["addr"].(string)
	objs := []any{}
	if h := d.blu().heard[addr]; h != nil {
		var keys [][2]int
		for k := range h.objects {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
		})
		for _, k := range keys {
			var comp any
			for sk, sc := range d.x.virtual {
				if strings.HasPrefix(sk, "bthomesensor:") && sc["addr"] == addr && sc["obj_id"] == float64(k[0]) && sc["idx"] == float64(k[1]) {
					comp = sk
				}
			}
			objs = append(objs, map[string]any{"obj_id": float64(k[0]), "idx": float64(k[1]), "component": comp})
		}
	}
	return map[string]any{"id": f, "objects": objs}, nil
}

// discover sends a device_discovered event for each sender heard, then
// discovery_done.
func (s *Sim) discover(d *device) {
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	b := d.blu()
	var found []map[string]any
	for _, h := range b.heard {
		dev := map[string]any{"addr": h.addr, "local_name": h.name, "rssi": float64(h.rssi), "encrypted": h.encrypted}
		if h.mf != nil {
			dev["shelly_mfdata"] = h.mf
		}
		found = append(found, dev)
	}
	b.disc = nil
	s.mu.Unlock()
	sort.Slice(found, func(i, j int) bool { return found[i]["addr"].(string) < found[j]["addr"].(string) })
	for _, dev := range found {
		ts := float64(time.Now().UnixMilli()) / 1000
		s.notify(d, "NotifyEvent", map[string]any{"ts": ts, "events": []any{map[string]any{"component": "bthome", "event": "device_discovered", "device": dev, "ts": ts}}})
	}
	ts := float64(time.Now().UnixMilli()) / 1000
	s.notify(d, "NotifyEvent", map[string]any{"ts": ts, "events": []any{map[string]any{"component": "bthome", "event": "discovery_done", "device_count": float64(len(found)), "ts": ts}}})
}

// Hear makes Gen2+ device key receive a Bluetooth advertisement (the
// advertising data, then any scan response) from addr: running scripts
// that relay advertisements emit it as a "ble.scan_result" event; a
// BTHomeDevice added for addr decodes it, updates its status and its
// sensors, and raises button and dimmer events and their webhooks.
func (s *Sim) Hear(key, addr string, rssi int, adv []byte) {
	d := s.find(key)
	if d == nil {
		return
	}
	addr = normAddr(addr)
	ads, _ := bthome.ParseAD(adv)
	s.mu.Lock()
	b := d.blu()
	h := b.heard[addr]
	if h == nil {
		h = &bluHeard{addr: addr, objects: map[[2]int]bool{}}
		b.heard[addr] = h
	}
	h.rssi = rssi
	if n := bthome.LocalName(ads); n != "" {
		h.name = n
	}
	if sd, ok := bthome.ServiceData16(ads)[bthome.UUID]; ok && len(sd) > 0 {
		h.encrypted = sd[0]&bthome.FlagEncrypted != 0
	}
	if mf, ok := bthome.Manufacturer(ads)[0x0BA9]; ok {
		h.mf = shellyMf(mf)
	}
	var relays []int
	for id, sc := range d.ext().scripts {
		if sc.running && strings.Contains(sc.code, "ble.scan_result") {
			relays = append(relays, id)
		}
	}
	devKey := s.bthomeDeviceKeyLocked(d, addr)
	bindKey := b.keys[addr]
	s.mu.Unlock()

	sort.Ints(relays)
	for _, id := range relays {
		ts := float64(time.Now().UnixMilli()) / 1000
		data := []any{2.0, []any{[]any{addr, float64(rssi), base64.StdEncoding.EncodeToString(adv), ""}}}
		s.notify(d, "NotifyEvent", map[string]any{"ts": ts, "events": []any{map[string]any{"component": "script:" + strconv.Itoa(id), "id": float64(id),
			"event": "ble.scan_result", "data": data, "ts": ts}}})
	}
	if devKey == "" {
		return
	}
	s.bthomeUpdate(d, devKey, addr, rssi, adv, bindKey)
}

func shellyMf(d []byte) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(d); {
		t := d[i]
		i++
		switch {
		case t == 0x01 && i+2 <= len(d):
			out["flags"] = float64(int(d[i]) | int(d[i+1])<<8)
			i += 2
		case t == 0x0B && i+2 <= len(d):
			out["model_id"] = float64(int(d[i]) | int(d[i+1])<<8)
			i += 2
		case t == 0x0A && i+6 <= len(d):
			var parts []string
			for k := 5; k >= 0; k-- {
				parts = append(parts, hex.EncodeToString(d[i+k:i+k+1]))
			}
			out["mac"] = strings.Join(parts, ":")
			i += 6
		default:
			return out
		}
	}
	return out
}

// bthomeUpdate applies an advertisement to a BTHomeDevice and its sensors.
func (s *Sim) bthomeUpdate(d *device, devKey, addr string, rssi int, adv []byte, bindKey []byte) {
	_, devID, _ := strings.Cut(devKey, ":")
	id, _ := strconv.Atoi(devID)
	pk, err := bthome.DecodeAdvertising(adv, addr, bindKey)
	now := float64(time.Now().UnixMilli()) / 1000
	if err != nil {
		reason := "parse_failed"
		switch err {
		case bthome.ErrNoKey:
			reason = "key_missing_or_bad"
		case bthome.ErrAuth:
			reason = "decrypt_failed"
		}
		s.SetComponent(d.key(), devKey, map[string]any{"errors": []any{reason}})
		return
	}
	s.mu.Lock()
	b := d.blu()
	if pk.Encrypted && !b.guard.Accept(addr, pk.Counter) {
		s.mu.Unlock()
		return
	}
	h := b.heard[addr]
	for _, o := range pk.Objects {
		h.objects[[2]int{int(o.ID), o.Index}] = true
	}
	devFields := map[string]any{"rssi": float64(rssi), "last_updated_ts": now, "paired": true}
	if pk.Encrypted || len(bindKey) > 0 {
		devFields["errors"] = []any{}
	}
	if o, ok := pk.Find(0x01, 0); ok {
		devFields["battery"] = o.Value
	}
	if pid, ok := pk.PacketID(); ok {
		devFields["packet_id"] = float64(pid)
	}
	if o, ok := pk.Find(0xF2, 0); ok {
		devFields["fw_ver"] = "v" + o.Text
	}
	channel := -1.0
	if o, ok := pk.Find(0x60, 0); ok {
		channel = o.Value
	}
	type change struct {
		key   string
		value any
		kind  bthome.Kind
	}
	var changes []change
	for sk, sc := range d.x.virtual {
		if !strings.HasPrefix(sk, "bthomesensor:") || sc["addr"] != addr {
			continue
		}
		oid, _ := sc["obj_id"].(float64)
		idx, _ := sc["idx"].(float64)
		o, ok := pk.Find(byte(oid), int(idx))
		if !ok {
			continue
		}
		var v any = o.Value
		switch o.Info.Kind {
		case bthome.Binary:
			v = o.Bool()
		case bthome.Text, bthome.Raw:
			v = o.Text
		}
		if d.cfg.Components[sk]["value"] != v {
			changes = append(changes, change{sk, v, o.Info.Kind})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].key < changes[j].key })
	var sensorsByObj = map[string][]any{}
	for sk, sc := range d.x.virtual {
		if strings.HasPrefix(sk, "bthomesensor:") && sc["addr"] == addr {
			oid, _ := sc["obj_id"].(float64)
			sensorsByObj[strconv.Itoa(int(oid))] = append(sensorsByObj[strconv.Itoa(int(oid))], clone(d.cfg.Components[sk]))
		}
	}
	s.mu.Unlock()

	s.SetComponent(d.key(), devKey, devFields)
	for _, c := range changes {
		s.SetComponent(d.key(), c.key, map[string]any{"value": c.value, "last_updated_ts": now})
		_, n, _ := strings.Cut(c.key, ":")
		cid, _ := strconv.Atoi(n)
		if c.kind == bthome.Binary {
			s.fireEv(d, "bthomesensor.state_change", cid, map[string]any{"state": c.value})
		} else {
			s.fireEv(d, "bthomesensor.value_change", cid, map[string]any{"value": c.value})
		}
	}
	var sensors any
	if len(sensorsByObj) > 0 {
		sensors = sensorsByObj
	}
	for _, o := range pk.Objects {
		if o.Info.Kind != bthome.Event || o.Event == "" {
			continue
		}
		name, ok := bthomeDeviceEvents[o.Event]
		if !ok {
			continue
		}
		ts := float64(time.Now().UnixMilli()) / 1000
		s.notify(d, "NotifyEvent", map[string]any{"ts": ts, "events": []any{map[string]any{"component": devKey, "id": float64(id),
			"event": name, "idx": float64(o.Index), "channel": channel, "sensors": sensors, "ts": ts}}})
		s.fireEv(d, "bthomedevice."+name, id, map[string]any{"idx": float64(o.Index)})
	}
}
