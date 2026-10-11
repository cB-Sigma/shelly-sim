# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.3.0 (2026-10-12)

Needs github.com/cB-Sigma/shelly v0.3.0 (its `bthome` package). The API
only grows.

### Added

- Webhook URL tokens are replaced before the call: `${ev.<attr>}`,
  `${status["<component>"].<field>}` and `${config...}`, null when a
  token does not resolve. Webhook.ListSupported names the attributes of
  temperature.change and humidity.change, which are now raised when
  tC or rh change.
- Gen1 action URLs are called: out_on_url and out_off_url when a relay
  changes, shortpush_url and longpush_url on `Press1`; `Report1` makes a
  sensor call its report_url with hum, temp, batV and id.
- Bluetooth: the BTHome component (BTHome.GetStatus, AddDevice with a
  key, DeleteDevice with its sensors, AddSensor, DeleteSensor,
  StartDeviceDiscovery with device_discovered and discovery_done events,
  GetObjectInfos, ResetEncryptionCounter; BTHomeDevice.GetKnownObjects;
  status, configuration and SetConfig of bthomedevice and bthomesensor
  components). `Hear` feeds a device an advertisement: a BTHomeDevice for
  the sender decodes it (with its key; replays dropped; decrypt_failed,
  key_missing_or_bad and parse_failed reported), updates battery, RSSI,
  packet id and firmware, updates its sensors, and raises button and
  dimmer events (single_push, ..., rotate_left) with their webhooks and
  bthomesensor value_change and state_change webhooks; running scripts
  that relay advertisements emit them as `ble.scan_result` events.
- Interop: aioshelly installs its relay script and reads the relayed
  advertisements, decoded by bthome-ble 3.24.0 (`TestBLE`).

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
- Gen1 /settings/<section>/<n> (values kept) and /settings/actions.
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
