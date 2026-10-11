// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package shelly_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cB-Sigma/shelly"
	"github.com/cB-Sigma/shelly-sim/sim"
)

// Ask a simulated Plus Plug S what it is, then switch its output off with
// JSON-RPC.
func Example_demoPlug() {
	s := sim.NewDemo() // stands in for the devices on a LAN
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base, _ := shelly.BaseURL(s.Addr("shellyplusplugs-a8032ab10003"))
	c := &shelly.Client{Base: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
	info, err := c.Shelly(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(info.DefaultID(), "gen", info.Generation(), info.ModelName())
	if _, err := c.RPC(ctx, "Switch.Set", map[string]any{"id": 0, "on": false}); err != nil {
		panic(err)
	}
	raw, _ := c.RPC(ctx, "Shelly.GetStatus", nil)
	var st map[string]map[string]any
	_ = json.Unmarshal(raw, &st)
	fmt.Println("output:", st["switch:0"]["output"])
	// Output:
	// shellyplusplugs-a8032ab10003 gen 2 SNPL-00112EU
	// output: false
}
