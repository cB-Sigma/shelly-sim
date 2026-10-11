// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"net"
	"testing"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
	"github.com/cB-Sigma/shelly/discover"
)

// The DNS-SD records the simulated devices announce, browsed through a
// stand-in for an mDNS browser, are found and confirmed by the discover
// package; each later-generation device reads with the typed views.
func TestDiscoverFullDemo(t *testing.T) {
	s := sim.NewFullDemo()
	defer s.Close()
	browser := func(_ context.Context, service string) ([]discover.Service, error) {
		var out []discover.Service
		for _, a := range s.Services() {
			if a.Service == service {
				out = append(out, discover.Service{Instance: a.Instance, Host: a.Host, Port: a.Port, IPs: []net.IP{a.IP}, TXT: a.TXT})
			}
		}
		return out, nil
	}
	found, err := discover.MDNS(ctx(t), browser, discover.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != len(s.Keys()) {
		t.Fatalf("found %d of %d devices", len(found), len(s.Keys()))
	}
	gens := map[int]int{}
	byID := map[string]discover.Device{}
	for _, d := range found {
		gens[d.Gen]++
		byID[d.ID] = d
	}
	if gens[1] != 2 || gens[3] != 4 || gens[4] != 1 {
		t.Fatalf("generations %v", gens)
	}
	c := func(id string) *shelly.Client { return client(s, id, "") }
	st, err := shelly.GetStatus(ctx(t), c("shellypro3em-a8032ab10014"))
	if err != nil {
		t.Fatal(err)
	}
	if em, ok := st.EM(0); !ok || *em.TotalActPower != 1575.3 || *em.B.ActPower != 480.1 {
		t.Fatalf("em %+v", em)
	}
	st, _ = shelly.GetStatus(ctx(t), c("shellypmminig3-a8032ab10015"))
	if pm, ok := st.PM1(0); !ok || *pm.APower != 1390 {
		t.Fatalf("pm1 %+v", pm)
	}
	st, _ = shelly.GetStatus(ctx(t), c("shellyemg3-a8032ab10016"))
	if e, ok := st.EM1(0); !ok || *e.ActPower != -2200 {
		t.Fatalf("em1 %+v", e)
	}
	on := false
	if err := shelly.SetLight(ctx(t), c("shellyplusrgbwpm-a8032ab10018"), "rgbw", 0, shelly.LightSet{On: &on}); err != nil {
		t.Fatal(err)
	}
	if info, _ := c("shelly1pmg4-a8032ab10013").Shelly(ctx(t)); info.Generation() != 4 {
		t.Fatalf("gen4 %+v", info)
	}
	_ = context.Background
}
