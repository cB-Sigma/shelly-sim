// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command shellysim serves simulated Shelly devices, so a Shelly client
// can be tried without devices. Each device is an HTTP server of its own
// on 127.0.0.1 (real devices each have port 80 on their own address):
//
//	shellyplus2pm-a8032ab10001  Plus 2PM, two switches with meters (Gen2)
//	shellyplus2pm-a8032ab10002  Plus 2PM in cover mode (Gen2)
//	shellyplusplugs-a8032ab10003  Plus Plug S (Gen2)
//	shellyplusht-a8032ab10004   Plus H&T (Gen2, battery)
//	98CDAC1F0005                Shelly 1PM (Gen1)
//	98CDAC1F0006                Dimmer 2 (Gen1)
//
// Gen2 devices answer GET /shelly, POST /rpc and ws://…/rpc; Gen1 devices
// the Gen1 HTTP API. With -coiot host:port the Gen1 devices send their
// CoIoT status message there every -interval, as real ones multicast it.
// With -all it also serves sim.NewFullDemo's Gen3, Gen4, metering,
// input, RGBW and timed cover devices; with -mqtt host:port every device
// connects to that MQTT broker in MQTT mode.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cB-Sigma/shelly-sim/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "shellysim:", err)
		os.Exit(1)
	}
}

// run serves until ctx ends. ready, when not nil, receives the simulator
// once it serves.
func run(ctx context.Context, args []string, out io.Writer, ready func(*sim.Sim)) error {
	fs := flag.NewFlagSet("shellysim", flag.ContinueOnError)
	fs.SetOutput(out)
	coiot := fs.String("coiot", "", "UDP host:port to send the Gen1 devices' CoIoT status messages to (empty: none)")
	interval := fs.Duration("interval", 10*time.Second, "how often CoIoT messages are sent")
	all := fs.Bool("all", false, "also serve the Gen3, Gen4, energy meter, input, RGBW and timed cover demo devices")
	broker := fs.String("mqtt", "", "MQTT broker host:port the devices connect to in MQTT mode (empty: none)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	if *coiot != "" {
		if _, err := net.ResolveUDPAddr("udp", *coiot); err != nil {
			return fmt.Errorf("-coiot: %w", err)
		}
		if *interval <= 0 {
			return errors.New("-interval must be positive")
		}
	}
	s := sim.NewDemo()
	if *all {
		s = sim.NewFullDemo()
	}
	defer s.Close()
	var gen1 []string
	for _, k := range s.Keys() {
		fmt.Fprintf(out, "shellysim: %-30s http://%s\n", k, s.Addr(k))
		if len(k) == 12 { // a Gen1 device's key is its MAC
			gen1 = append(gen1, k)
		}
	}
	if *broker != "" {
		for _, k := range s.Keys() {
			disconnect, err := s.ConnectMQTT(k, *broker, sim.MQTTOptions{})
			if err != nil {
				return fmt.Errorf("-mqtt: %s: %w", k, err)
			}
			defer disconnect()
		}
		fmt.Fprintf(out, "shellysim: devices in MQTT mode on %s\n", *broker)
	}
	if ready != nil {
		ready(s)
	}
	var tick <-chan time.Time
	if *coiot != "" {
		t := time.NewTicker(*interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
			for _, k := range gen1 {
				if err := s.SendCoIoT(k, *coiot); err != nil {
					fmt.Fprintln(out, "shellysim: CoIoT:", err)
				}
			}
		}
	}
}
