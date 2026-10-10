//go:build interop

// SPDX-License-Identifier: MIT

// Package interop runs black-box tests of go-modbus against independent
// Modbus TCP implementations: libmodbus, PyModbus, digitalpetri/modbus,
// NModbus and tokio-modbus, packaged as container images by
// otfabric/modbus-interop.
//
// Both directions are covered:
//
//   - the go-modbus client against the reference servers (client_test.go)
//   - the reference clients against a go-modbus server (server_test.go)
//
// The images are the only thing shared with modbus-interop: their container
// contract (commands, JSON documents, exit codes) and the fixture baked into
// them, which the tests read with "print-fixture". The scenarios, expected
// values and assertions are owned here. The go-modbus device the reference
// clients talk to is in fixture.go: a RequestHandler that serves a fixture
// the way the reference servers do.
//
// Build with -tags=interop. Running needs Docker; see README.md § Interop
// tests. Editor/gopls: add -tags=interop to buildFlags.
package interop
