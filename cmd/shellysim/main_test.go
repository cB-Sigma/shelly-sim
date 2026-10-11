// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

func TestRunner(t *testing.T) {
	l, err := shelly.ListenCoIoT(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, "none")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heard := make(chan string, 8)
	go func() { _ = l.Serve(ctx, func(m shelly.CoIoTMessage, _ *net.UDPAddr) { heard <- m.DevID }) }()
	got := make(chan *sim.Sim, 1)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-coiot", l.Addr(), "-interval", "50ms"}, &out, func(s *sim.Sim) { got <- s })
	}()
	var s *sim.Sim
	select {
	case s = <-got:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	c := &shelly.Client{Base: "http://" + s.Addr("shellyplus2pm-a8032ab10001"), HTTP: &http.Client{Timeout: 3 * time.Second}}
	if info, err := c.Shelly(ctx); err != nil || info.App != "Plus2PM" {
		t.Errorf("info = %+v %v", info, err)
	}
	select {
	case id := <-heard:
		if id != "98CDAC1F0005" && id != "98CDAC1F0006" {
			t.Errorf("heard %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Error("no CoIoT message")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "shellyplusht-a8032ab10004") || !strings.Contains(out.String(), "98CDAC1F0006") {
		t.Errorf("output = %s", out.String())
	}
}

func TestRunnerFlags(t *testing.T) {
	for _, args := range [][]string{{"-coiot", "nowhere:x"}, {"-coiot", "127.0.0.1:5683", "-interval", "0s"}, {"-bogus"}, {"extra"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, nil); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
