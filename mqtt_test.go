// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

// brokerPub publishes through an embedded broker's inline client.
type brokerPub struct{ b *mqtt.Server }

func (p brokerPub) Publish(_ context.Context, topic string, payload []byte, retain bool) error {
	return p.b.Publish(topic, payload, retain, 0)
}

// broker starts mochi-mqtt on a free loopback port and routes what the
// application's filters match into m.Handle; it returns the address and
// the messages Handle returned.
func broker(t *testing.T, m *shelly.MQTT) (string, func() []shelly.MQTTMessage) {
	t.Helper()
	b := mqtt.New(&mqtt.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := b.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })
	m.Pub = brokerPub{b}
	var mu sync.Mutex
	var got []shelly.MQTTMessage
	for i, f := range m.Filters() {
		if err := b.Subscribe(f, i+1, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
			r := m.Handle(pk.TopicName, pk.Payload)
			if r.Kind != shelly.MQTTNone {
				mu.Lock()
				got = append(got, r)
				mu.Unlock()
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	return tcp.Address(), func() []shelly.MQTTMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]shelly.MQTTMessage(nil), got...)
	}
}

// Simulated devices in MQTT mode on a real broker, read and commanded
// with the shelly package's MQTT type over the broker's inline client.
func TestMQTTOverBroker(t *testing.T) {
	m := &shelly.MQTT{Src: "sigma-test", Timeout: 3 * time.Second}
	addr, msgs := broker(t, m)
	s := sim.NewDemo()
	defer s.Close()

	stop2, err := s.ConnectMQTT(plus2pm, addr, sim.MQTTOptions{StatusNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stop2()
	dev := shelly.MQTTDevice{M: m, Root: plus2pm}
	waitFor(t, "the RPC subscription", func() bool {
		_, err := shelly.GetDeviceInfo(context.Background(), dev)
		return err == nil
	})
	if was, err := shelly.SetSwitch(ctx(t), dev, 1, true, 0); err != nil || was {
		t.Fatal(was, err)
	}
	waitFor(t, "a NotifyStatus over MQTT", func() bool {
		for _, r := range msgs() {
			if r.Kind == shelly.MQTTNotification && r.Method == "NotifyStatus" && strings.Contains(string(r.Params), `"switch:1"`) {
				return true
			}
		}
		return false
	})
	if _, err := shelly.SetSwitch(ctx(t), dev, 9, true, 0); !shelly.IsRPCError(err, -105) {
		t.Fatalf("error over MQTT: %v", err)
	}
	if _, _, err := shelly.KVSSet(ctx(t), dev, "via", "mqtt", ""); err != nil {
		t.Fatal(err)
	}

	stop1, err := s.ConnectMQTT(g1pm, addr, sim.MQTTOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop1()
	id1 := sim.Gen1MQTTID("SHSW-PM", g1pm)
	waitFor(t, "the Gen1 announcement and relay state", func() bool {
		ann, relay := false, false
		for _, r := range msgs() {
			ann = ann || (r.Kind == shelly.MQTTAnnounce && r.Announce.ID == id1 && r.Announce.Model == "SHSW-PM")
			relay = relay || (r.Kind == shelly.MQTTGen1 && r.Device == id1 && strings.Join(r.Topic, "/") == "relay/0")
		}
		return ann && relay
	})
	if err := m.Command1(ctx(t), "shellies/"+id1, shelly.Gen1Turn("relay", 0, "off")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the relay to go off", func() bool { return !s.Gen1(g1pm).Relays[0].On })
}
