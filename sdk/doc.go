// Package mesh provides the embeddable GopherMesh engine used to load config,
// expose HTTP/TCP routes, cold-start local backends, and run the dashboard.
//
// The recommended integration order is:
//
//  1. Start with CLI + config.json when you only need routing and process orchestration.
//  2. Use this package when you need to embed GopherMesh into a custom Go launcher.
//
// This package currently implements the single-node gateway/runtime model.
// It should not be described as a full distributed service mesh.
package mesh
