# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.2.0 (2026-10-11)

Needs github.com/cB-Sigma/shelly v0.2.0 for its tests. The API only
grows.

### Added

- Per-component configuration in the shape devices answer, kept across
  `<Type>.SetConfig` (with cfg_rev and restart_required), and
  `<Type>.GetConfig`; Shelly.ListMethods; Shelly.GetComponents in pages
  and with dynamic components.
- Scripts, schedules (`RunSchedule`), webhooks called on changes and
  input events (`Hooks`), the key-value store (`KVS`), virtual
  components, `<Type>.ResetCounters`, Input.Trigger; RGB, RGBW and CCT
  Set and Toggle.
- `Device.CoverTravel`: covers that move over time with opening and
  closing states.
- `ConnectMQTT`: Gen1 and Gen2+ devices in MQTT mode on a real broker,
  over a small MQTT 3.1.1 client of the module's own.
- `NewFullDemo` (Gen3, Gen4, energy meters, inputs, RGBW, timed cover);
  `NewDemo` is unchanged. `Services`: each device's DNS-SD records.
- Command: `-all` and `-mqtt`.
- Interop: aioshelly against the additions (`TestGen2More`), and
  announcing and discovering over multicast with cB-Sigma/mdns.
- Test dependency: mochi-mqtt v2.7.9 (MIT), for the embedded broker in
  the tests only.

### Changed

- Shelly.GetConfig now holds the common configuration fields and the
  wifi, cloud, mqtt and ble sections, not only id and name.
- Cover.GoToPosition to 100 or 0 ends in state open or closed (stopped
  before), as Cover.Open and Cover.Close did.

## v0.1.0 (2026-10-11)

First release as a repository of its own, split from SigmaM2M: the
simulator (`sim`), its command (`cmd/shellysim`) and the tests of the
github.com/cB-Sigma/shelly client that drive it. Needs
github.com/cB-Sigma/shelly v0.1.0 for its tests.

Changed while checking it against aioshelly 13.34.1 (`interop/`):

- Over WebSocket, Shelly.GetDeviceInfo is answered without
  authentication, as over HTTP; a channel receives notifications only
  once authenticated.
- Gen1 lights are described in /cit/d (block `light_<n>`: output, and
  brightness, or red, green, blue and gain in colour mode) and reported
  in CoIoT messages.
- New answers: Shelly.GetConfig (a configuration per component, the
  device name under `sys`, the outbound WebSocket under `ws`),
  Shelly.GetComponents (no dynamic components), `<Component>.GetStatus`,
  Shelly.Update with `stage` (installs what `SetUpdate` offers), Gen1
  /reboot (counted by `Reboots`).
- Gen1 /settings gives `mode` "color" or "white" for an RGBW2 or Bulb.
- `NewHost`: devices listen on another address than 127.0.0.1.
