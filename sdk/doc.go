// Package mesh provides the embeddable GopherMesh engine and launcher helpers
// used to load config, expose HTTP/TCP routes, bridge request-driven stdio
// workers in explicit stream/http modes, cold-start local backends, run the
// dashboard, and integrate the gophermesh:// desktop bootstrap flow. The
// launch protocol supports
// gophermesh://launch with optional port/conf query parameters.
//
// The recommended integration order is:
//
//  1. Start with CLI + config.json when you only need routing and process orchestration.
//  2. Use this package when you need to embed GopherMesh into a custom Go launcher.
//
// This package currently implements the single-node gateway/runtime model.
// It should not be described as a full distributed service mesh.
package mesh
