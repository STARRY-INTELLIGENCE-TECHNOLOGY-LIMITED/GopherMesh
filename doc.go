// Command gophermesh runs the GopherMesh local/edge/server mesh gateway and
// process orchestrator.
//
// By default it loads config from "config.json", starts the dashboard,
// exposes configured HTTP/TCP public ports, and optionally cold-starts local
// backends on demand.
//
// Basic usage:
//
//	gophermesh -config config.json
//
// To embed the engine in another Go program, use the sdk package:
//
//	github.com/SUTFutureCoder/gophermesh/sdk
package main
