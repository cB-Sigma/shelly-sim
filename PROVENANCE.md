# Provenance

## How the code was written

Written in Go for SigmaM2M, a oneM2M platform, as the test double of its
Shelly integration, and then moved out with the protocol code into
github.com/cB-Sigma/shelly. No code was ported or copied from another
implementation, and no source file of another project was translated.

The simulated devices follow Shelly's public API documentation
(shelly-api-docs.shelly.cloud) for facts: paths, method and field names,
error codes, topic layouts, CoAP option numbers, the authentication forms
and the BLE characteristic UUIDs. Where the documents are silent, the
simulator makes choices of its own (README, "Choices of the simulator").

History:

- 2026-09-27: Gen1 HTTP and Gen2+ JSON-RPC over HTTP and WebSocket with
  authentication and notifications, BLE RPC, as part of SigmaM2M.
- 2026-10-05: CoIoT messages, Gen1 MQTT topics and commands, Gen2+ RPC for
  MQTT, the outbound WebSocket of sleeping devices, Wi-Fi setup and
  firmware updates; the Gen1 colour-mode light.
- 2026-10-06: moved into the shelly module as `sim` and `cmd/shellysim`.
- 2026-10-11: moved into this module of its own, with the tests of the
  shelly client that drive it, and checked against aioshelly (below).
- 2026-10-11 (v0.2.0): configuration, scripts, schedules, webhooks, KVS,
  virtual components, counters, timed covers and the method list
  (`sim/gen2more.go`); MQTT mode on a broker with an MQTT 3.1.1 client of
  our own (`sim/mqtt.go`); the fifteen-device demo and DNS-SD records
  (`sim/demo.go`). Model codes and app names of the Gen3 and Gen4
  devices come from Shelly's device documents and Home Assistant's
  fixtures (facts only). How aioshelly reads scripts (pages by byte
  offset) and decides script support (Shelly.ListMethods) was read from
  its source, then checked by running it.
- 2026-10-12 (v0.3.0): webhook URL tokens and Gen1 action and report URLs
  (`sim/hooks.go`), the BTHome component and `Hear` (`sim/bthome.go`).
  Facts from Shelly's Webhook, BTHome, BTHomeDevice and BTHomeSensor
  pages and the Gen1 action documents; the `ble.scan_result` event form
  from aioshelly 13.34.1. BTHome packets are decoded with the shelly
  module's `bthome` package (shared code with the library; that package
  is checked against bthome-ble separately). Checked by running aioshelly
  and bthome-ble against it (`TestBLE`, 12 checks, all passed on the
  first run).

## Sources

| Area (files) | Followed (facts) |
|---|---|
| Gen1 HTTP (sim/sim.go) | Gen1 API docs: /shelly, /status, /settings, /settings/sta, /relay, /light, /color, /roller, /ota, /reboot, basic authentication |
| Gen2+ RPC (sim/sim.go) | Gen2+ docs: Shelly.GetDeviceInfo, GetStatus, GetConfig, GetComponents, Update, Reboot; Switch, Light, Cover, Ws, WiFi methods; error codes; SHA-256 digest; RFC 7616 |
| WebSocket channel (sim/sim.go) | Gen2+ docs: RPC over WebSocket, the authentication object, NotifyStatus and NotifyEvent |
| CoIoT, MQTT, outbound WebSocket (sim/push.go) | Gen1 docs: CoIoT v2, MQTT topics; Gen2+ docs: MQTT channel, the Ws component; RFC 7252 |
| BLE RPC (sim/sim.go) | Gen2+ docs: BLE RPC service and characteristics |
| Configuration and the rest of the RPC API (sim/gen2more.go) | Gen2+ docs: component configurations, Script, Schedule, Webhook, KVS, Virtual and virtual component types, ResetCounters, Input.Trigger, Shelly.ListMethods |
| MQTT client (sim/mqtt.go) | OASIS MQTT 3.1.1: CONNECT with a will, SUBSCRIBE, PUBLISH at QoS 0 and 1, PUBACK, PINGREQ, DISCONNECT; Gen2+ docs: MQTT channel topics |
| DNS-SD records (sim/demo.go) | Gen2+ docs: the mDNS service and its TXT keys; RFC 6763 |

## Independent verification

`interop/` runs aioshelly 13.34.1 (Home Assistant's Shelly library, in
Python, written independently of this module) in a python:3.12.10-slim
container against the simulated devices. Run on 2026-10-11 on macOS with
Docker Desktop (arm64); all 17 Gen1 and 24 Gen2 checks passed after the
fixes below:

- Gen1 (`BlockDevice`, basic authentication): a wrong password refused;
  /shelly, /settings, /status and /cit/d read; values taken from CoIoT
  messages the simulator sent to aioshelly's socket (output, power,
  energy, temperature); a relay switched off; a CoAP status request
  answered by the next message; a firmware update by URL; a reboot; a
  Dimmer 2's brightness; an RGBW2 in colour mode set through the path
  aioshelly takes from /settings.
- Gen2+ (`RpcDevice` over WebSocket with the authentication object): a
  wrong password refused; device info, config (the device's name) and
  status read; a switch set and the NotifyStatus that followed; a
  NotifyEvent; Wi-Fi settings; the outbound WebSocket set (with the
  restart aioshelly asks for); polling; a firmware update from the stable
  channel; an RPC error code; a cover moved and read back.
- JSON-RPC over HTTP with aiohttp's own SHA-256 digest middleware: three
  calls answered, a wrong password refused, the challenge's form.
- A simulated sleeping device opened its outbound WebSocket to aioshelly's
  server, which took it as the device coming online.

Found and fixed (each with a regression test in `sim/sim_test.go` or
`push_test.go`):

- Over WebSocket, the simulator asked for authentication on
  Shelly.GetDeviceInfo, which devices answer without it (as the simulator
  already did over HTTP); aioshelly reads it first to learn the realm and
  could not log in (`TestWebSocketDeviceInfoWithoutAuth`).
- Gen1 lights had no blocks in /cit/d and no values in CoIoT messages, so
  a CoIoT client found no light (`TestCoIoTLights`).
- Missing answers a client uses while setting a device up:
  Shelly.GetConfig, Shelly.GetComponents, `<Component>.GetStatus`,
  Shelly.Update by release channel, Gen1 /reboot, and the colour or white
  mode of a Gen1 RGBW2 or Bulb in /settings
  (`TestSetupCallsOfOtherClients`).
- Devices could listen only on 127.0.0.1; `NewHost` takes another address
  (`TestNewHost`).

v0.2.0, run on 2026-10-11 in the same image: the Gen1 and Gen2 runs
again (all passed), and `TestGen2More`, 35 checks of aioshelly's script,
KVS, virtual and dynamic component calls, schedules and webhooks through
call_rpc, configuration, Gen3 and Gen4 devices, a Pro 3EM and a timed
cover (all passed after one fix). Found and fixed: a virtual
component's Set over WebSocket deadlocked, since the change was notified
while the asking channel still held the device's notification order
(`TestVirtualSetOverWebSocket`). Also run: the devices' DNS-SD records
announced and browsed over multicast with github.com/cB-Sigma/mdns
(`TestMDNSAnnounceAndDiscover`), and MQTT mode against an embedded
mochi-mqtt broker (`TestMQTTOverBroker`).

Not done: a comparison with a real device.

## Passages to review before publication

No passage is known to be copied. The documentation is the only source,
so a check is about prose: the comments describe the protocol in our own
words.
