// Command gophermesh runs the GopherMesh local/edge/server mesh gateway and
// process orchestrator.
//
// By default it loads config from "config.json", starts the dashboard,
// exposes configured HTTP/TCP public routes, optionally cold-starts local
// backends on demand, supports request-driven stdio workers for portless
// sandbox/FaaS-style execution via explicit stream/http stdio bridge modes,
// and best-effort registers the
// gophermesh:// launch protocol unless disabled with -noprotocol.
// Protocol URLs support gophermesh://launch with optional port/conf query
// parameters.
//
// Basic usage:
//
//	gophermesh -config config.json
//
// To embed the engine in another Go program, use the sdk package:
//
//	github.com/SUTFutureCoder/gophermesh/sdk
package main
