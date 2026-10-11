// Test-only module: aioshelly against github.com/cB-Sigma/shelly-sim's
// simulated devices, in Docker (see doc.go). Not part of the published
// module.
module github.com/cB-Sigma/shelly-sim/interop

go 1.27.1

replace github.com/cB-Sigma/shelly-sim => ../

require (
	github.com/cB-Sigma/mdns v0.2.0
	github.com/cB-Sigma/shelly v0.2.0
	github.com/cB-Sigma/shelly-sim v0.0.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
