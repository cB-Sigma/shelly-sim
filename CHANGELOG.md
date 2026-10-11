# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

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
