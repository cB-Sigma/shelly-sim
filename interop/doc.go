// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package interop holds tests only: aioshelly, the Shelly library of Home
// Assistant (Python, Apache-2.0), written independently of this module,
// drives the simulated devices of github.com/cB-Sigma/shelly-sim: Gen1
// over HTTP with basic authentication and CoIoT, Gen2+ JSON-RPC over
// WebSocket with the authentication object, JSON-RPC over HTTP with
// aiohttp's own SHA-256 digest, notifications, and a simulated sleeping
// device opening its outbound WebSocket to aioshelly's server.
//
// The devices run in the test process; aioshelly runs in a pinned Python
// image, which installs the pinned aioshelly package from PyPI and runs
// testdata/aioshelly_peer.py, our own script. No aioshelly code is kept in
// this repository. The tests run only when SIGMA_SHELLY_INTEROP=1 and
// docker is on PATH, and need network access for the package:
//
//	cd interop && SIGMA_SHELLY_INTEROP=1 GOWORK=off go test -v -timeout 20m ./...
//
// The container reaches the devices at host.docker.internal. On macOS
// (Docker Desktop) the devices listen on 127.0.0.1; elsewhere on all
// interfaces for the duration of a test, since a Linux container reaches
// the host through the bridge. The test reaches the container's CoIoT
// socket and WebSocket server through ports published on 127.0.0.1.
package interop
