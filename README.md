# shelly-sim

Simulated Shelly devices, Gen1 and Gen2+, on their local APIs, for testing Shelly clients without devices; not affiliated with Shelly Group.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/shelly-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/shelly-sim) [![test](https://github.com/cB-Sigma/shelly-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/shelly-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.24-00ADD8.svg)](go.mod)

Each simulated device is an HTTP server of its own, started in-process
from a Go test or from the command. It answers the way the documented
local APIs do, including authentication and pushes, and records what it
was asked so a test can check it. Any client can use it, in any
language; it is checked against aioshelly, Home Assistant's Shelly
library. It was written for SigmaM2M, a oneM2M platform, as the test
double of its Shelly integration.

## What is simulated

| Feature | Status |
|---|---|
| GET /shelly for both generations | yes |
| Gen1 HTTP: /status, /settings (with the light mode), /settings/sta, /relay, /light, /color, /roller, /ota, /reboot, /cit/d; basic authentication | yes |
| Gen2+ JSON-RPC over HTTP (POST /rpc) with SHA-256 digest; Shelly.GetDeviceInfo without authentication | yes |
| Gen2+ JSON-RPC over WebSocket (ws://host/rpc) with the authentication object; NotifyStatus on changes, NotifyEvent on demand | yes |
| Gen2+ methods | Shelly.GetDeviceInfo, GetStatus, GetConfig, GetComponents, Update (by URL or stage), Reboot; Switch.Set and Toggle, Light.Set and Toggle, Cover.Open, Close, Stop, GoToPosition; `<Component>.GetStatus`; Ws.GetConfig and SetConfig; WiFi.SetConfig. Others answer -114 |
| Gen1 CoIoT status messages, with a /cit/d description of relays, lights, inputs and the device's sensors | yes, sent when the test asks (`SendCoIoT`) |
| Gen1 MQTT: the topics a device publishes, relay commands | yes, as values and calls (no broker) |
| Gen2+ JSON-RPC as over MQTT | yes, as a call (`RPC`; no broker) |
| Outbound WebSocket of a sleeping Gen2+ device: NotifyFullStatus, then changes | yes (`DialOutbound`) |
| BLE RPC as a GATT peer (length and data characteristics, 20-octet chunks) | yes (`BLE`) |
| Wi-Fi setup and firmware updates | yes; an update installs the version `SetUpdate` offers |
| Faults | a device offline (503, channels dropped), dropped WebSocket channels |
| Counters | calls per method or path, control requests, authentication failures, reboots, WebSocket clients, BLE writes |

### Choices of the simulator

Where the documents are silent or devices differ, the simulator makes
choices of its own; tests should not take them for real-device
behaviour:

- Devices listen on 127.0.0.1 (or the address given to `NewHost`), each
  on its own port; real devices each have port 80 on their own address.
- CoIoT sensor IDs follow the simulator's own numbering. Real devices
  number them differently by model, which is why clients must read
  /cit/d.
- The WebSocket nonce is the current Unix time; HTTP nonces are random
  and stay valid for the device's life.
- Shelly.GetConfig gives only `id` and `name` per component, the device
  name and MAC under `sys` and the outbound WebSocket under `ws`.
  Shelly.GetComponents lists no dynamic (virtual) components.
- A firmware update installs 100 ms after it is asked for. A roller or
  cover reaches its position at once.
- A device without `sys` in its components has none in its status; real
  devices always have it, so configure one when a client expects it.

## Install

```
go get github.com/cB-Sigma/shelly-sim@latest
```

Requires Go 1.24 or later.

## Command

```
go run github.com/cB-Sigma/shelly-sim/cmd/shellysim@latest -coiot 127.0.0.1:5683 -interval 5s
```

| Flag | Default | Meaning |
|---|---|---|
| `-coiot` | (none) | UDP host:port to send the Gen1 devices' CoIoT status messages to |
| `-interval` | 10s | how often the CoIoT messages are sent |

It serves the six devices of `sim.NewDemo` and prints one line per
device, with ports chosen freely on each run:

```
shellysim: shellyplus2pm-a8032ab10001     http://127.0.0.1:<port>
shellysim: shellyplus2pm-a8032ab10002     http://127.0.0.1:<port>
shellysim: shellyplusplugs-a8032ab10003   http://127.0.0.1:<port>
shellysim: shellyplusht-a8032ab10004      http://127.0.0.1:<port>
shellysim: 98CDAC1F0005                   http://127.0.0.1:<port>
shellysim: 98CDAC1F0006                   http://127.0.0.1:<port>
```

They are a Plus 2PM with two switches, a Plus 2PM in cover mode, a Plus
Plug S, a Plus H&T, a Gen1 Shelly 1PM and a Gen1 Dimmer 2. The command
stops on Ctrl-C or SIGTERM.

## Quick start: in Go tests

Read a simulated Plus Plug S with
[shelly](https://github.com/cB-Sigma/shelly) and switch it off
(`Example_demoPlug` in example_test.go, run by `go test`):

```go
s := sim.NewDemo() // stands in for the devices on a LAN
defer s.Close()

base, _ := shelly.BaseURL(s.Addr("shellyplusplugs-a8032ab10003"))
c := &shelly.Client{Base: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
info, _ := c.Shelly(ctx)
fmt.Println(info.DefaultID(), "gen", info.Generation(), info.ModelName())
_, _ = c.RPC(ctx, "Switch.Set", map[string]any{"id": 0, "on": false})
// Output:
// shellyplusplugs-a8032ab10003 gen 2 SNPL-00112EU
```

Devices of your own, changes, events and checks:

```go
s := sim.New()
defer s.Close()
s.Add(sim.Device{Gen: 2, ID: "shellyplus1-a8032ab10b01", MAC: "A8032AB10B01", Model: "SNSW-001X16EU",
	App: "Plus1", Password: "secret",
	Components: map[string]map[string]any{"switch:0": {"id": 0.0, "output": false}, "sys": {"uptime": 1.0}}})
s.Add(sim.Device{Gen: 1, MAC: "98CDAC1F0A01", Model: "SHSW-1", Relays: []sim.Relay1{{}}})

s.SetComponent("shellyplus1-a8032ab10b01", "switch:0", map[string]any{"output": true}) // NotifyStatus
s.Event("shellyplus1-a8032ab10b01", "input:0", "single_push")                          // NotifyEvent
_ = s.SendCoIoT("98CDAC1F0A01", listenerAddr)                                           // a CoIoT message
s.SetOffline("98CDAC1F0A01", true)
n := s.AuthFailures()
reqs := s.Requests() // "<id> Switch.Set {...}", "<mac> /relay/0?turn=on"
```

## Documentation

[pkg.go.dev/github.com/cB-Sigma/shelly-sim](https://pkg.go.dev/github.com/cB-Sigma/shelly-sim)

| Package | What it does |
|---|---|
| `sim` | `Sim` with `New`, `NewHost`, `NewDemo`, `Add`; device state and pushes (`SetComponent`, `Event`, `SetGen1`, `Press1`, `SendCoIoT`, `Gen1Topics`, `MQTTCommand`, `RPC`, `DialOutbound`, `BLE`); faults; counters and records (`Calls`, `Requests`, `AuthFailures`, `WSClients`, `WiFi`, `UpdateURL`, `Reboots`, `WsConfig`) |
| `cmd/shellysim` | Serves the six demo devices, optionally sending CoIoT messages |
| `interop` (nested module, tests only) | aioshelly against the simulator, in Docker |

Changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Testing

```
go test -race ./...
```

The tests cover the simulator itself, the command, and the shelly
client's requests, digest and basic authentication, the WebSocket
channel, the outbound WebSocket, CoIoT, MQTT, BLE RPC, Wi-Fi setup and
firmware updates against the simulator.

`interop/` is a nested module with its own go.mod. Its tests run
aioshelly in a Python container and are skipped unless
`SIGMA_SHELLY_INTEROP=1` is set and docker is on PATH; they need network
access for the package:

```
cd interop && SIGMA_SHELLY_INTEROP=1 go test -v -timeout 20m ./...
```

### Interop tested against

- aioshelly 13.34.1 (Home Assistant's Shelly library, Python, written
  independently of this module), `interop`, 2026-10-11, in
  python:3.12.10-slim on Docker Desktop (arm64): all 17 Gen1 and 24 Gen2
  checks passed. Gen1: basic authentication (a wrong password refused),
  /shelly, /settings, /status and /cit/d, values from CoIoT messages
  sent to aioshelly's socket, a relay, a Dimmer 2's brightness, an RGBW2
  in colour mode, a firmware update by URL, a reboot. Gen2+ over
  WebSocket with the authentication object (a wrong password refused):
  device info, config and status, a switch with the NotifyStatus that
  followed, a NotifyEvent, Wi-Fi settings, the outbound WebSocket
  setting with the restart, polling, an update from the stable channel,
  an RPC error code, a cover. JSON-RPC over HTTP with aiohttp's own
  SHA-256 digest middleware. A simulated sleeping device's outbound
  WebSocket to aioshelly's server. The run found a WebSocket
  authentication bug and missing answers in the simulator, fixed in
  v0.1.0 (CHANGELOG.md, PROVENANCE.md).
- The [shelly](https://github.com/cB-Sigma/shelly) client, in this
  module's tests (written by the same author, so not independent).
- Not yet: a comparison with a real device.

## Demo values

Every device id, MAC address, name, password, Wi-Fi network, firmware
version, reading and address in this module is a made-up demo value.
There are no keys or credentials.

## Related repositories

- [shelly](https://github.com/cB-Sigma/shelly): the Go client for the
  Shelly local APIs that this module tests.

## Status

v0.x; the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. Shelly is a trademark of Shelly Group; the name is used
only to say which devices this module simulates.
