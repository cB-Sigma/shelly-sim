// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/url"
	"strconv"
	"strings"
)

// Outgoing calls: Gen2+ webhook URLs with their ${...} tokens replaced,
// and Gen1 action and sensor report URLs.

// eventAttrs are the attributes Webhook.ListSupported names for events
// that carry values (the ${ev.<name>} tokens).
var eventAttrs = map[string][]any{
	"temperature.change":        {map[string]any{"name": "tC", "type": "number", "desc": "°C"}, map[string]any{"name": "tF", "type": "number", "desc": "°F"}},
	"humidity.change":           {map[string]any{"name": "rh", "type": "number", "desc": "%"}},
	"bthomesensor.value_change": {map[string]any{"name": "value", "type": "number", "desc": "new value"}},
	"bthomesensor.state_change": {map[string]any{"name": "state", "type": "boolean", "desc": "new state"}},
}

// expandTokensLocked replaces ${ev.x}, ${status...} and ${config...}
// tokens in u (s.mu held). A token that does not resolve is replaced with
// null, as devices do.
func (s *Sim) expandTokensLocked(d *device, u string, ev map[string]any) string {
	if !strings.Contains(u, "${") {
		return u
	}
	var b strings.Builder
	for {
		i := strings.Index(u, "${")
		if i < 0 {
			b.WriteString(u)
			break
		}
		j := strings.Index(u[i:], "}")
		if j < 0 {
			b.WriteString(u)
			break
		}
		b.WriteString(u[:i])
		b.WriteString(tokenText(s.resolveLocked(d, u[i+2:i+j], ev)))
		u = u[i+j+1:]
	}
	return b.String()
}

func tokenText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return url.QueryEscape(x)
	}
	return "null"
}

// tokenPath splits `status["switch:0"].apower` into status, switch:0,
// apower.
func tokenPath(expr string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '.':
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		case '[':
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			end := strings.IndexByte(expr[i:], ']')
			if end < 0 {
				return out
			}
			out = append(out, strings.Trim(expr[i+1:i+end], `"'`))
			i += end
		default:
			cur += string(expr[i])
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (s *Sim) resolveLocked(d *device, expr string, ev map[string]any) any {
	p := tokenPath(strings.TrimSpace(expr))
	if len(p) < 2 {
		return nil
	}
	var cur any
	switch p[0] {
	case "ev":
		cur = map[string]any(ev)
	case "status":
		st := map[string]any{}
		for k, v := range d.cfg.Components {
			st[k] = map[string]any(v)
		}
		cur = st
	case "config":
		cur = s.configLocked(d)
	default:
		return nil
	}
	for _, k := range p[1:] {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// action1Locked calls the enabled URLs of Gen1 action name for output or
// input index (s.mu held), with extra query parameters, in the
// background.
func (s *Sim) action1Locked(d *device, name string, index int, extra url.Values) {
	acts := d.g1["actions"]
	list, _ := acts[name].([]any)
	var urls []string
	for _, a := range list {
		m, _ := a.(map[string]any)
		if m == nil || m["enabled"] != true {
			continue
		}
		if idx, _ := m["index"].(float64); int(idx) != index {
			continue
		}
		us, _ := m["urls"].([]any)
		for _, u := range us {
			str, _ := u.(string)
			if str == "" {
				continue
			}
			if len(extra) > 0 {
				sep := "?"
				if strings.Contains(str, "?") {
					sep = "&"
				}
				str += sep + extra.Encode()
			}
			urls = append(urls, str)
		}
	}
	if len(urls) == 0 {
		return
	}
	x := d.ext()
	x.hookCalls = append(x.hookCalls, urls...)
	for _, u := range urls {
		go func(u string) {
			resp, err := hookHTTP.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}(u)
	}
}

// Report1 makes a Gen1 sensor call its report URL (report_url) with its
// readings and id, as an H&T does when it wakes: hum and temp, plus
// batV for a battery device.
func (s *Sim) Report1(key string) {
	d := s.find(key)
	if d == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := d.cfg
	q := url.Values{}
	if c.Hum != nil {
		q.Set("hum", strconv.FormatFloat(*c.Hum, 'f', -1, 64))
	}
	if c.TempC != nil {
		q.Set("temp", strconv.FormatFloat(*c.TempC, 'f', 2, 64))
	}
	if c.Battery != nil {
		q.Set("batV", strconv.FormatFloat(*c.Battery, 'f', 2, 64))
	}
	id := strings.ToLower(c.Model)
	if c.ID != "" {
		id = c.ID
	}
	q.Set("id", id)
	s.action1Locked(d, "report_url", 0, q)
}
