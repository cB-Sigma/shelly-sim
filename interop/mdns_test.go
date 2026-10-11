// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"context"
	"math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/cB-Sigma/mdns"
	"github.com/cB-Sigma/shelly-sim/sim"
	"github.com/cB-Sigma/shelly/discover"
)

// The simulated devices' DNS-SD records announced over real multicast
// with github.com/cB-Sigma/mdns (on a test port, so the operating
// system's responder is not involved), browsed with the same module
// through discover.MDNS, and confirmed with GET /shelly. This is the
// wiring the README shows; it needs no Docker.
func TestMDNSAnnounceAndDiscover(t *testing.T) {
	if testing.Short() {
		t.Skip("uses multicast")
	}
	port := 20000 + rand.IntN(30000)
	ifi, err := mdns.TestInterface(port)
	if err != nil {
		t.Skipf("no multicast interface: %v", err)
	}
	opts := mdns.Options{Interfaces: []net.Interface{*ifi}, Port: port, Loopback: true, Wait: 2 * time.Second}
	s := sim.NewDemo()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, a := range s.Services() {
		an, err := mdns.Announce(ctx, mdns.Service{Instance: a.Instance, Host: a.Host, Port: a.Port, TXT: a.TXT, IPs: []net.IP{a.IP}}, a.Service, opts)
		if err != nil {
			t.Fatalf("announce %s %s: %v", a.Instance, a.Service, err)
		}
		defer an.Close()
	}
	browse := func(ctx context.Context, service string) ([]discover.Service, error) {
		list, err := mdns.Browse(ctx, service, opts)
		out := make([]discover.Service, 0, len(list))
		for _, x := range list {
			out = append(out, discover.Service{Instance: x.Instance, Host: x.Host, Port: x.Port, IPs: x.IPs, TXT: x.TXT})
		}
		return out, err
	}
	found, err := discover.MDNS(ctx, browse, discover.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range found {
		ids[d.ID] = true
		t.Logf("found %s gen %d %s at %s", d.ID, d.Gen, d.Model, d.Addr)
	}
	for _, want := range []string{"shellyplus2pm-a8032ab10001", "shellyplusht-a8032ab10004", "shelly-98cdac1f0005", "shelly-98cdac1f0006"} {
		if !ids[want] {
			t.Errorf("%s not found", want)
		}
	}
	if len(found) != len(s.Keys()) {
		t.Errorf("found %d of %d devices", len(found), len(s.Keys()))
	}
}
