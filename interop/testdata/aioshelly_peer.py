# Copyright 2026 Abhinav Gautam
# SPDX-License-Identifier: Apache-2.0
#
# Drives aioshelly (Home Assistant's Shelly library, Apache-2.0, installed
# from PyPI in the container) against devices of github.com/cB-Sigma/shelly-sim
# running in the Go test process. Prints one JSON line per check:
# {"check": name, "ok": bool, "detail": text}. Our own script; no aioshelly
# code is kept here.
#
#   python -I aioshelly_peer.py gen1 --host H --relay PORT,MAC --dimmer PORT,MAC --rgbw PORT,MAC --password PW
#   python -I aioshelly_peer.py gen2 --host H --switch PORT,MAC --cover PORT,MAC --password PW --ws-port N
#
# aioshelly reaches Gen1 devices on port 80 only, so each Gen1 device gets a
# loopback address of the container (127.0.0.2, ...) with a TCP forwarder on
# port 80 to the simulator's port on the host.

import argparse
import asyncio
import json
import sys
import traceback

import aiohttp
from aiohttp import DigestAuthMiddleware

from aioshelly.block_device import COAP, BlockDevice
from aioshelly.common import ConnectionOptions
from aioshelly.exceptions import InvalidAuthError, RpcCallError
from aioshelly.rpc_device import RpcDevice, RpcUpdateType, WsServer

FAILED = False


def report(check, ok, detail=""):
    global FAILED
    if not ok:
        FAILED = True
    print(json.dumps({"check": check, "ok": bool(ok), "detail": str(detail)}), flush=True)


async def expect(check, coro_fn, detail_fn=None):
    """Run a step; a raised exception is a failed check."""
    try:
        result = await coro_fn()
    except Exception as err:  # noqa: BLE001
        report(check, False, f"{type(err).__name__}: {err}")
        return None
    report(check, True, detail_fn(result) if detail_fn else "")
    return result


def port_mac(v):
    port, mac = v.split(",")
    return int(port), mac


async def forward(local_ip, local_port, host, port):
    async def pipe(reader, writer):
        try:
            while data := await reader.read(65536):
                writer.write(data)
                await writer.drain()
        except (ConnectionError, asyncio.CancelledError):
            pass
        finally:
            writer.close()

    async def handle(r1, w1):
        try:
            r2, w2 = await asyncio.open_connection(host, port)
        except OSError:
            w1.close()
            return
        await asyncio.gather(pipe(r1, w2), pipe(r2, w1))

    return await asyncio.start_server(handle, local_ip, local_port)


async def gen1(a):
    relay_port, relay_mac = port_mac(a.relay)
    dim_port, dim_mac = port_mac(a.dimmer)
    rgbw_port, rgbw_mac = port_mac(a.rgbw)
    servers = [
        await forward("127.0.0.2", 80, a.host, relay_port),
        await forward("127.0.0.3", 80, a.host, dim_port),
        await forward("127.0.0.4", 80, a.host, rgbw_port),
    ]
    coap = COAP()
    await coap.initialize(5683)
    async with aiohttp.ClientSession() as session:
        # A wrong password is refused.
        bad = await BlockDevice.create(session, coap, ConnectionOptions("127.0.0.2", "admin", "wrong", device_mac=relay_mac))
        try:
            await bad.initialize()
            report("gen1_wrong_password", False, "initialize succeeded")
        except InvalidAuthError as err:
            report("gen1_wrong_password", True, f"InvalidAuthError: {err}")
        except Exception as err:  # noqa: BLE001
            report("gen1_wrong_password", False, f"{type(err).__name__}: {err}")
        await bad.shutdown()

        # The relay device with basic authentication, values from CoIoT.
        dev = await BlockDevice.create(session, coap, ConnectionOptions("127.0.0.2", "admin", a.password, device_mac=relay_mac))
        ok = await expect("gen1_initialize", dev.initialize)
        if ok is None and not dev.initialized:
            return
        report("gen1_shelly", dev.model == "SHSW-PM" and dev.requires_auth and dev.firmware_version.endswith("v1.14.0-gcb84623") and dev.firmware_supported,
               f"model={dev.model} auth={dev.requires_auth} fw={dev.firmware_version} supported={dev.firmware_supported}")
        report("gen1_settings_status", dev.hostname != "" and isinstance(dev.status.get("relays"), list),
               f"hostname={dev.hostname} relays={dev.status.get('relays')}")
        blocks = {b.description: b for b in dev.blocks}
        relay = blocks.get("relay_0")
        device = blocks.get("device")
        report("gen1_cit_d_blocks", relay is not None and device is not None and "input_0" in blocks,
               f"blocks={sorted(blocks)}")
        if relay is not None and device is not None:
            report("gen1_coiot_values", relay.output == 1 and relay.power == 60.5 and relay.energy == 120000 and device.temperature == 21.5,
                   f"output={relay.output} power={relay.power} energy={relay.energy} temperature={device.temperature} coap_s={dev.coap_s}")
            res = await expect("gen1_relay_off", lambda: relay.set_state(turn="off"), lambda r: r)
            if res is not None:
                report("gen1_relay_answer", res.get("ison") is False, res)
        await expect("gen1_coap_update", dev.update)
        await expect("gen1_ota_url", lambda: dev.trigger_ota_update(url="http://files.example.invalid/fw.zip"), lambda r: r)
        await expect("gen1_reboot", dev.trigger_reboot)
        await dev.shutdown()

        # A dimmer: brightness on /light/0.
        dim = await BlockDevice.create(session, coap, ConnectionOptions("127.0.0.3", device_mac=dim_mac))
        if await expect("gen1_dimmer_initialize", dim.initialize) is not None or dim.initialized:
            light = next((b for b in dim.blocks if b.type == "light"), None)
            report("gen1_dimmer_block", light is not None, f"blocks={[b.description for b in dim.blocks]}")
            if light is not None:
                await expect("gen1_dimmer_brightness", lambda: light.set_state(turn="on", brightness=30), lambda r: r)
        await dim.shutdown()

        # An RGBW2 in colour mode: aioshelly takes the path from /settings mode.
        rgbw = await BlockDevice.create(session, coap, ConnectionOptions("127.0.0.4", device_mac=rgbw_mac))
        if await expect("gen1_rgbw_initialize", rgbw.initialize) is not None or rgbw.initialized:
            light = next((b for b in rgbw.blocks if b.type == "light"), None)
            report("gen1_rgbw_mode", rgbw.settings.get("mode") == "color" and light is not None, f"mode={rgbw.settings.get('mode')}")
            if light is not None:
                await expect("gen1_rgbw_gain", lambda: light.set_state(turn="on", gain=25), lambda r: r)
        await rgbw.shutdown()
    coap.close()
    for s in servers:
        s.close()


async def gen2(a):
    sw_port, sw_mac = port_mac(a.switch)
    cv_port, cv_mac = port_mac(a.cover)
    ws_server = WsServer()
    await ws_server.initialize(a.ws_port)
    async with aiohttp.ClientSession() as session:
        # JSON-RPC over HTTP with aiohttp's own digest implementation.
        url = f"http://{a.host}:{sw_port}/rpc"
        body = {"id": 1, "method": "Shelly.GetStatus"}
        async with aiohttp.ClientSession(middlewares=(DigestAuthMiddleware("admin", "wrong"),)) as s2:
            async with s2.post(url, json=body) as resp:
                report("gen2_http_digest_wrong_password", resp.status == 401, f"HTTP {resp.status}")
        async with aiohttp.ClientSession(middlewares=(DigestAuthMiddleware("admin", a.password),)) as s2:
            for i in range(3):
                async with s2.post(url, json={"id": 10 + i, "method": "Shelly.GetStatus"}) as resp:
                    data = await resp.json(content_type=None)
                    report(f"gen2_http_digest_{i}", resp.status == 200 and "switch:0" in data.get("result", {}),
                           f"HTTP {resp.status} {json.dumps(data)[:200]}")
        async with session.post(url, json=body) as resp:
            report("gen2_http_no_auth_challenge", resp.status == 401 and "SHA-256" in resp.headers.get("WWW-Authenticate", ""),
                   f"HTTP {resp.status} {resp.headers.get('WWW-Authenticate')}")

        # WebSocket RPC with the authentication object.
        bad = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, "admin", "wrong", port=sw_port, device_mac=sw_mac))
        try:
            await bad.initialize()
            report("gen2_ws_wrong_password", False, "initialize succeeded")
        except InvalidAuthError as err:
            report("gen2_ws_wrong_password", True, f"InvalidAuthError: {err}")
        except Exception as err:  # noqa: BLE001
            report("gen2_ws_wrong_password", False, f"{type(err).__name__}: {err}")
        await bad.shutdown()

        dev = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, "admin", a.password, port=sw_port, device_mac=sw_mac))
        updates = []
        got_status = asyncio.Event()
        got_event = asyncio.Event()

        def on_update(_dev, kind):
            updates.append(kind)
            if kind == RpcUpdateType.STATUS:
                got_status.set()
            if kind == RpcUpdateType.EVENT:
                got_event.set()

        dev.subscribe_updates(on_update)
        if await expect("gen2_ws_initialize", dev.initialize) is None and not dev.initialized:
            return
        report("gen2_device_info", dev.gen == 2 and dev.model == "SNSW-001P16EU" and dev.hostname == "shellyplus1pm-a8032ab1e001" and dev.requires_auth and dev.firmware_supported,
               f"gen={dev.gen} model={dev.model} id={dev.hostname} auth={dev.requires_auth} fw={dev.firmware_version} supported={dev.firmware_supported}")
        report("gen2_config_name", dev.name == "Porch", f"name={dev.name}")
        report("gen2_status", dev.status.get("switch:0", {}).get("output") is False and "sys" in dev.status, f"switch:0={dev.status.get('switch:0')}")
        await expect("gen2_switch_set", lambda: dev.switch_set(0, True))
        try:
            await asyncio.wait_for(got_status.wait(), 5)
            report("gen2_notify_status", dev.status["switch:0"]["output"] is True, f"switch:0={dev.status['switch:0']}")
        except TimeoutError:
            report("gen2_notify_status", False, f"no NotifyStatus; updates={updates}")
        print(json.dumps({"check": "waiting_for_event", "ok": True, "detail": ""}), flush=True)
        try:
            await asyncio.wait_for(got_event.wait(), 10)
            evs = (dev.event or {}).get("events", [])
            report("gen2_notify_event", any(e.get("event") == "single_push" and e.get("component") == "input:0" for e in evs), json.dumps(dev.event))
        except TimeoutError:
            report("gen2_notify_event", False, f"no NotifyEvent; updates={updates}")
        await expect("gen2_wifi_setconfig", lambda: dev.wifi_setconfig(sta_ssid="Site", sta_password="password1", sta_enable=True), lambda r: r)
        restarted = await expect("gen2_outbound_ws_config", lambda: dev.update_outbound_websocket(f"ws://{a.server_ip}:{a.ws_port}/api/shelly/ws"), lambda r: f"restarted={r}")
        report("gen2_outbound_ws_restart", restarted is True, f"restarted={restarted}")
        await expect("gen2_poll", dev.poll)
        await expect("gen2_ota_stable", dev.trigger_ota_update)
        try:
            await dev.call_rpc("Switch.Set", {"id": 7, "on": True})
            report("gen2_rpc_error", False, "accepted")
        except RpcCallError as err:
            report("gen2_rpc_error", err.code == -105, f"code={err.code} {err}")
        await dev.shutdown()

        # A cover without a password.
        cov = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, port=cv_port, device_mac=cv_mac))
        if await expect("gen2_cover_initialize", cov.initialize) is not None or cov.initialized:
            await expect("gen2_cover_position", lambda: cov.cover_set_position(0, pos=70))
            await expect("gen2_cover_status", lambda: cov.update_cover_status(0), lambda r: cov.status["cover:0"])
            report("gen2_cover_value", cov.status["cover:0"].get("current_pos") == 70, cov.status["cover:0"])
        await cov.shutdown()

        # The outbound WebSocket: a sleeping device (the sim) connects to
        # aioshelly's server, which hands its frames to the device object.
        ht_mac = a.sleeper
        sleeper = RpcDevice(ws_server, session, ConnectionOptions(a.host, port=1, device_mac=ht_mac))
        online = asyncio.Event()
        sleeper.subscribe_updates(lambda _d, kind: online.set() if kind == RpcUpdateType.ONLINE else None)
        print(json.dumps({"check": "waiting_for_outbound", "ok": True, "detail": ""}), flush=True)
        try:
            await asyncio.wait_for(online.wait(), 10)
            report("gen2_outbound_ws_online", True, "")
        except TimeoutError:
            report("gen2_outbound_ws_online", False, "no frame from the sleeping device")
    ws_server.close()
    await asyncio.sleep(0.2)


async def gen2more(a):
    """v0.2.0 additions: method list, scripts, KVS, virtual components,
    per-component configuration, a Gen3 and a Gen4 device, energy meters,
    a timed cover's notifications."""
    ws_server = WsServer()
    await ws_server.initialize(a.ws_port)
    async with aiohttp.ClientSession() as session:
        sw_port, sw_mac = port_mac(a.switch)
        dev = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, "admin", a.password, port=sw_port, device_mac=sw_mac))
        updates = []
        dev.subscribe_updates(lambda _d, kind: updates.append(kind))
        if await expect("more_initialize", dev.initialize) is None and not dev.initialized:
            return
        report("more_switch_config", dev.config.get("switch:0", {}).get("in_mode") == "follow" and "wifi" in dev.config and "mqtt" in dev.config,
               json.dumps(dev.config.get("switch:0")))
        await expect("more_supports_scripts", dev.supports_scripts, lambda r: f"supports={r}")
        await expect("more_script_create", lambda: dev.script_create("sigma"))
        scripts = await expect("more_script_list", dev.script_list, lambda r: json.dumps(r))
        sid = scripts[0]["id"] if scripts else 1
        code = "// sigma interop\n" + "let n = 0; // ünï\n" * 200
        await expect("more_script_putcode", lambda: dev.script_putcode(sid, code))
        got, offset = "", 0
        for _ in range(20):
            page = await dev.script_getcode(sid, offset)
            got += page["data"]
            offset = len(got.encode())
            if page["left"] == 0:
                break
        report("more_script_getcode", got == code, f"{len(got)} of {len(code)} characters")
        await expect("more_script_start", lambda: dev.script_start(sid))
        res = await expect("more_script_eval", lambda: dev.script_eval(sid, "40 + 2"), lambda r: json.dumps(r))
        report("more_script_eval_result", res is not None and res.get("result") == "42", json.dumps(res))
        await expect("more_script_stop", lambda: dev.script_stop(sid))
        await expect("more_kvs_set", lambda: dev.kvs_set("sigma", {"mode": "eco", "n": 3}))
        kv = await expect("more_kvs_get", lambda: dev.kvs_get("sigma"), lambda r: json.dumps(r))
        report("more_kvs_value", kv is not None and kv.get("value") == {"mode": "eco", "n": 3}, json.dumps(kv))
        added = await expect("more_virtual_add", lambda: dev.call_rpc("Virtual.Add", {"type": "number", "config": {"name": "Setpoint", "min": 10, "max": 30}}),
                             lambda r: json.dumps(r))
        vid = (added or {}).get("id", 200)
        await expect("more_number_set", lambda: dev.number_set(vid, 22.5))
        await expect("more_dynamic_components", dev.get_dynamic_components)
        report("more_dynamic_status", dev.status.get(f"number:{vid}", {}).get("value") == 22.5 and dev.config.get(f"number:{vid}", {}).get("name") == "Setpoint",
               f"status={dev.status.get(f'number:{vid}')} config={dev.config.get(f'number:{vid}')}")
        try:
            await dev.number_set(vid, 99)
            report("more_number_range", False, "accepted 99")
        except RpcCallError as err:
            report("more_number_range", err.code == -103, f"code={err.code}")
        job = await expect("more_schedule_create", lambda: dev.call_rpc("Schedule.Create", {"enable": True, "timespec": "0 0 7 * * *",
                           "calls": [{"method": "Switch.Set", "params": {"id": 0, "on": True}}]}), lambda r: json.dumps(r))
        jobs = await expect("more_schedule_list", lambda: dev.call_rpc("Schedule.List"), lambda r: json.dumps(r))
        report("more_schedule_listed", jobs is not None and job is not None and jobs["jobs"][0]["id"] == job["id"], json.dumps(jobs))
        hook = await expect("more_webhook_create", lambda: dev.call_rpc("Webhook.Create", {"cid": 0, "enable": True, "event": "switch.on",
                            "urls": ["http://192.0.2.1/x"]}), lambda r: json.dumps(r))
        report("more_webhook_id", hook is not None and hook.get("id") == 1, json.dumps(hook))
        await dev.shutdown()

        for name, spec in (("gen3", a.gen3), ("gen4", a.gen4)):
            port, mac = port_mac(spec)
            d = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, port=port, device_mac=mac))
            if await expect(f"more_{name}_initialize", d.initialize) is not None or d.initialized:
                report(f"more_{name}_gen", d.gen == int(name[-1]) and "switch:0" in d.status, f"gen={d.gen} model={d.model}")
                await expect(f"more_{name}_switch", lambda: d.switch_set(0, True))
            await d.shutdown()

        port, mac = port_mac(a.meter)
        em = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, port=port, device_mac=mac))
        if await expect("more_em_initialize", em.initialize) is not None or em.initialized:
            e = em.status.get("em:0", {})
            report("more_em_values", e.get("total_act_power") == 1575.3 and "emdata:0" in em.status, json.dumps(e)[:200])
        await em.shutdown()

        port, mac = port_mac(a.cover)
        cov = await RpcDevice.create(session, ws_server, ConnectionOptions(a.host, port=port, device_mac=mac))
        states = []
        done = asyncio.Event()

        def on_cover(_d, kind):
            if kind == RpcUpdateType.STATUS:
                st = cov.status.get("cover:0", {}).get("state")
                if not states or states[-1] != st:
                    states.append(st)
                if st == "closed":
                    done.set()

        cov.subscribe_updates(on_cover)
        if await expect("more_cover_initialize", cov.initialize) is not None or cov.initialized:
            await expect("more_cover_close", lambda: cov.cover_close(0))
            try:
                await asyncio.wait_for(done.wait(), 10)
            except TimeoutError:
                pass
            report("more_cover_timed", "closing" in states and states[-1] == "closed" and cov.status["cover:0"].get("current_pos") == 0,
                   f"states={states} pos={cov.status['cover:0'].get('current_pos')}")
        await cov.shutdown()
    ws_server.close()
    await asyncio.sleep(0.2)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("mode", choices=["gen1", "gen2", "gen2more"])
    p.add_argument("--gen3")
    p.add_argument("--gen4")
    p.add_argument("--meter")
    p.add_argument("--host", required=True)
    p.add_argument("--password", default="")
    p.add_argument("--relay")
    p.add_argument("--dimmer")
    p.add_argument("--rgbw")
    p.add_argument("--switch")
    p.add_argument("--cover")
    p.add_argument("--sleeper")
    p.add_argument("--ws-port", type=int, default=8123)
    p.add_argument("--server-ip", default="127.0.0.1")
    a = p.parse_args()
    try:
        asyncio.run({"gen1": gen1, "gen2": gen2, "gen2more": gen2more}[a.mode](a))
    except Exception as err:  # noqa: BLE001
        report("run", False, f"{type(err).__name__}: {err}\n{traceback.format_exc()}")
    sys.exit(1 if FAILED else 0)


if __name__ == "__main__":
    main()
