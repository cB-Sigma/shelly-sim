// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"testing"
	"time"

	"github.com/cB-Sigma/shelly-sim/sim"
	"github.com/cB-Sigma/shelly/bthome"
)

// v0.3.0: aioshelly installs its Bluetooth relay script on a simulated
// Gen3 device and reads the ble.scan_result events the device sends when
// it hears a BLU door sensor; bthome-ble decodes the relayed BTHome data
// and aioshelly the Shelly manufacturer data; aioshelly adds a
// BTHomeDevice and a BTHomeSensor through call_rpc and sees the sensor's
// value in its status.
func TestBLE(t *testing.T) {
	needPeer(t)
	s := sim.NewHost(listenHost())
	defer s.Close()
	const gw, blu = "shelly1pmminig3-a8032ab1e201", "3c:2e:f5:71:d5:2a"
	s.Add(sim.Device{Gen: 3, ID: gw, MAC: "A8032AB1E201", Model: "S3SW-001P8EU", App: "Mini1PMG3", FW: "1.4.4",
		Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}, "sys": {"uptime": 3.0}}})

	var b bthome.Builder
	sd, err := b.Add(0x00, 3).Add(0x01, 87).Add(0x05, 140.5).Add(0x2D, 1).ServiceData()
	if err != nil {
		t.Fatal(err)
	}
	adv := bthome.Advertising(sd, "SBDW-002C")
	// Shelly manufacturer data: flags 0x0019, model id 2, the device's MAC
	// (sent last byte first).
	mf := []byte{0x01, 0x19, 0x00, 0x0B, 0x02, 0x00, 0x0A, 0x2A, 0xD5, 0x71, 0xF5, 0x2E, 0x3C}
	adv = append(adv, append([]byte{byte(3 + len(mf)), 0xFF, 0xA9, 0x0B}, mf...)...)

	got := peer(t, func(c check) {
		if c.Check == "waiting_for_adverts" {
			go func() {
				for i := 0; i < 3; i++ {
					time.Sleep(300 * time.Millisecond)
					s.Hear(gw, blu, -61, adv)
				}
			}()
		}
	}, "ble", "--host", "host.docker.internal", "--ws-port", wsPort, "--switch", port(t, s.Addr(gw))+",A8032AB1E201", "--blu", blu)
	want(t, got, "ble_initialize", "ble_ensure_enabled", "ble_start_scanner", "ble_script_running", "ble_bthome_add_device", "ble_bthome_add_sensor",
		"ble_scan_result_event", "ble_scan_result_parsed", "ble_shelly_mfdata", "ble_bthome_decoded", "ble_bthome_sensor_status", "ble_bthome_device_status")
}
