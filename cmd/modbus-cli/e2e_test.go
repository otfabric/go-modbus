// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
)

// testHandler is an in-memory Modbus device. Addresses at or above limit are
// illegal; unit IDs listed in silent get no response, those in failing a
// server device failure.
type testHandler struct {
	mu       sync.Mutex
	coils    [65536]bool
	di       [65536]bool
	holding  [65536]uint16
	input    [65536]uint16
	limit    uint32
	unitIDs  []uint8
	silent   map[uint8]bool
	failing  map[uint8]bool
	requests atomic.Int64
	// onRequest, when set, is called with the request count before each
	// request is served.
	onRequest func(n int64)
}

func newTestHandler() *testHandler {
	return &testHandler{limit: 0x10000, silent: map[uint8]bool{}, failing: map[uint8]bool{}}
}

func (h *testHandler) check(unitID uint8, addr, qty uint16) error {
	n := h.requests.Add(1)
	if h.onRequest != nil {
		h.onRequest(n)
	}
	h.unitIDs = append(h.unitIDs, unitID)
	if h.silent[unitID] {
		return modbus.ErrNoResponse
	}
	if h.failing[unitID] {
		return modbus.ErrServerDeviceFailure
	}
	if uint32(addr)+uint32(qty) > h.limit {
		return modbus.ErrIllegalDataAddress
	}
	return nil
}

func (h *testHandler) HandleCoils(_ context.Context, req *modbus.CoilsRequest) ([]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.check(req.UnitID, req.Addr, req.Quantity); err != nil {
		return nil, err
	}
	if req.IsWrite {
		for i, v := range req.Args {
			h.coils[int(req.Addr)+i] = v
		}
		return nil, nil
	}
	return append([]bool(nil), h.coils[req.Addr:int(req.Addr)+int(req.Quantity)]...), nil
}

func (h *testHandler) HandleDiscreteInputs(_ context.Context, req *modbus.DiscreteInputsRequest) ([]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.check(req.UnitID, req.Addr, req.Quantity); err != nil {
		return nil, err
	}
	return append([]bool(nil), h.di[req.Addr:int(req.Addr)+int(req.Quantity)]...), nil
}

func (h *testHandler) HandleHoldingRegisters(_ context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.check(req.UnitID, req.Addr, req.Quantity); err != nil {
		return nil, err
	}
	if req.IsWrite {
		copy(h.holding[req.Addr:], req.Args)
		return nil, nil
	}
	return append([]uint16(nil), h.holding[req.Addr:int(req.Addr)+int(req.Quantity)]...), nil
}

func (h *testHandler) HandleInputRegisters(_ context.Context, req *modbus.InputRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.check(req.UnitID, req.Addr, req.Quantity); err != nil {
		return nil, err
	}
	return append([]uint16(nil), h.input[req.Addr:int(req.Addr)+int(req.Quantity)]...), nil
}

func (h *testHandler) holdingRegs(addr, n int) []uint16 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint16(nil), h.holding[addr:addr+n]...)
}

func (h *testHandler) seenUnitIDs() []uint8 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint8(nil), h.unitIDs...)
}

// startServer runs h on a free loopback port and returns the target URL.
func startServer(t *testing.T, h *testHandler) string {
	t.Helper()

	var lastErr error
	for range 5 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		srv, err := modbus.NewServer(&modbus.ServerConfig{URL: "tcp://" + addr, Timeout: 10 * time.Second}, h)
		if err != nil {
			t.Fatal(err)
		}
		if lastErr = srv.Start(); lastErr == nil {
			t.Cleanup(func() { _ = srv.Stop() })
			return "tcp://" + addr
		}
	}
	t.Fatalf("failed to start server: %v", lastErr)
	return ""
}

// runCLI runs the command line and returns stdout, stderr and the exit code.
func runCLI(args ...string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func expectRun(t *testing.T, wantCode int, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, code := runCLI(args...)
	if code != wantCode {
		t.Fatalf("%v: exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, wantCode, stdout, stderr)
	}
	return stdout, stderr
}

func expectLines(t *testing.T, got string, want ...string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), got)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, lines[i], want[i])
		}
	}
}

func decodeJSONLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestCLI_VersionAndHelp(t *testing.T) {
	stdout, _ := expectRun(t, 0, "version")
	if !strings.HasPrefix(stdout, "modbus-cli dev (commit ") || strings.Count(stdout, "\n") != 1 {
		t.Errorf("unexpected version output:\n%s", stdout)
	}

	stdout, _ = expectRun(t, 0)
	if !strings.Contains(stdout, "Available operations:") {
		t.Errorf("help not printed:\n%s", stdout)
	}

	_, stderr := expectRun(t, ExitUsage, "version", "extra")
	if !strings.Contains(stderr, "Error:") {
		t.Errorf("unexpected stderr: %q", stderr)
	}

	_, stderr = expectRun(t, ExitUsage, "--no-such-flag")
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("unexpected stderr: %q", stderr)
	}

	_, stderr = expectRun(t, ExitUsage, "--unit-id", "-1", "rc:0")
	if !strings.Contains(stderr, "invalid argument") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestCLI_ArgumentErrors(t *testing.T) {
	// nothing listens here: every case must fail before connecting
	const target = "tcp://127.0.0.1:1"
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a pem file"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		wantErr  string
		wantCode int
	}{
		{"no target", []string{"rc:0"}, "no target specified", ExitUsage},
		{"parity", []string{"--target", target, "--parity", "mark", "rc:0"}, "unknown parity setting \"mark\"", ExitUsage},
		{"data bits", []string{"--target", target, "--data-bits", "9", "rc:0"}, "data bits 9 out of range", ExitUsage},
		{"stop bits", []string{"--target", target, "--stop-bits", "3", "rc:0"}, "stop bits 3 out of range", ExitUsage},
		{"timeout syntax", []string{"--target", target, "--timeout", "soon", "rc:0"}, "failed to parse timeout", ExitUsage},
		{"timeout zero", []string{"--target", target, "--timeout", "0s", "rc:0"}, "must be greater than 0", ExitUsage},
		{"timeout negative", []string{"--target", target, "--timeout", "-1s", "rc:0"}, "must be greater than 0", ExitUsage},
		{"unit id", []string{"--target", target, "--unit-id", "256", "rc:0"}, "unit ID 256 out of range", ExitUsage},
		{"endianness", []string{"--target", target, "--endianness", "middle", "rc:0"}, "unknown endianness", ExitUsage},
		{"word order", []string{"--target", target, "--word-order", "mf", "rc:0"}, "unknown word order", ExitUsage},
		{"operation", []string{"--target", target, "rc:0+65535"}, "exceeds the limit of 2000", ExitUsage},
		// used to wrap 16385*4 registers to 4 and panic slicing the reply
		{"wrapped register count", []string{"--target", target, "rh:uint64:0+16384"}, "exceeds the limit of 31", ExitUsage},
		{"tls no cert", []string{"--target", "tcp+tls://127.0.0.1:1", "rc:0"}, "use --cert", ExitUsage},
		{"tls no key", []string{"--target", "tcp+tls://127.0.0.1:1", "--cert", junk, "rc:0"}, "use --key", ExitUsage},
		{"tls no ca", []string{"--target", "tcp+tls://127.0.0.1:1", "--cert", junk, "--key", junk, "rc:0"}, "use --ca", ExitUsage},
		{"tls bad pair", []string{"--target", "tcp+tls://127.0.0.1:1", "--cert", junk, "--key", junk, "--ca", junk, "rc:0"}, "failed to load client TLS key pair", ExitInvalidInput},
		{"bad url", []string{"--target", "foo://x", "rc:0"}, "failed to create client", ExitUsage},
		{"connect", []string{"--target", target, "--timeout", "200ms", "rc:0"}, "failed to open client", ExitTransport},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := expectRun(t, tt.wantCode, tt.args...)
			if stdout != "" {
				t.Errorf("unexpected stdout: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "Error: ") || !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr %q does not contain %q", stderr, tt.wantErr)
			}
		})
	}
}

func TestCLI_TLSBadCA(t *testing.T) {
	// a valid key pair but an unusable CA file
	dir := t.TempDir()
	certPEM, keyPEM := selfSignedPair(t)
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	if err := os.WriteFile(cert, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr := expectRun(t, ExitInvalidInput, "--target", "tcp+tls://127.0.0.1:1", "--cert", cert, "--key", key,
		"--ca", filepath.Join(dir, "missing.pem"), "rc:0")
	if !strings.Contains(stderr, "failed to load TLS CA/server certificate") {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

func TestCLI_ReadBools(t *testing.T) {
	h := newTestHandler()
	h.coils[1] = true
	h.di[0xfffe] = true
	target := startServer(t, h)

	stdout, stderr := expectRun(t, 0, "--target", target, "rc:0+2", "rdi:0xfffe+1")
	if stderr != "" {
		t.Errorf("unexpected stderr: %q", stderr)
	}
	expectLines(t, stdout,
		"0x0000\t0     : false",
		"0x0001\t1     : true",
		"0x0002\t2     : false",
		"0xfffe\t65534 : true",
		"0xffff\t65535 : false",
	)
}

func TestCLI_ReadRegisters(t *testing.T) {
	h := newTestHandler()
	copy(h.holding[0:], []uint16{0xfffe, 0x1234, 0x5678, 0x3fc0, 0x0000})
	copy(h.holding[0x10:], []uint16{0x0102, 0x0304, 0x0506, 0x0708})
	copy(h.holding[0x20:], []uint16{0xbff8, 0, 0, 0}) // float64 -1.5
	copy(h.input[0:], []uint16{0x8000})
	target := startServer(t, h)

	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"rh:uint16:0+1"}, []string{"0x0000\t0     : 0xfffe\t65534", "0x0001\t1     : 0x1234\t4660"}},
		{[]string{"rh:int16:0"}, []string{"0x0000\t0     : 0xfffe\t-2"}},
		{[]string{"ri:int16:0"}, []string{"0x0000\t0     : 0x8000\t-32768"}},
		{[]string{"--endianness", "little", "rh:uint16:1"}, []string{"0x0001\t1     : 0x3412\t13330"}},
		{[]string{"rh:uint32:1"}, []string{"0x0001\t1     : 0x12345678\t305419896"}},
		{[]string{"--word-order", "lf", "rh:uint32:1"}, []string{"0x0001\t1     : 0x56781234\t1450709556"}},
		{[]string{"--endianness", "little", "rh:uint32:1"}, []string{"0x0001\t1     : 0x34127856\t873625686"}},
		{[]string{"--endianness", "little", "--word-order", "lowfirst", "rh:uint32:1"}, []string{"0x0001\t1     : 0x78563412\t2018915346"}},
		{[]string{"rh:int32:0"}, []string{"0x0000\t0     : 0xfffe1234\t-126412"}},
		{[]string{"rh:float32:3"}, []string{"0x0003\t3     : 1.500000"}},
		{[]string{"rh:uint32:1+1"}, []string{"0x0001\t1     : 0x12345678\t305419896", "0x0003\t3     : 0x3fc00000\t1069547520"}},
		{[]string{"rh:uint64:0x10"}, []string{"0x0010\t16    : 0x0102030405060708\t72623859790382856"}},
		{[]string{"--word-order", "lf", "rh:uint64:0x10"}, []string{"0x0010\t16    : 0x0708050603040102\t506660481457717506"}},
		// little endian 64-bit reads used to fail with "unknown codec"
		{[]string{"--endianness", "little", "rh:uint64:0x10"}, []string{"0x0010\t16    : 0x0201040306050807\t144401074084972551"}},
		{[]string{"--endianness", "little", "--word-order", "lf", "rh:uint64:0x10"}, []string{"0x0010\t16    : 0x0807060504030201\t578437695752307201"}},
		{[]string{"rh:int64:0x20"}, []string{"0x0020\t32    : 0xbff8000000000000\t-4613937818241073152"}},
		{[]string{"rh:float64:0x20"}, []string{"0x0020\t32    : -1.500000"}},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			stdout, _ := expectRun(t, 0, append([]string{"--target", target}, tt.args...)...)
			expectLines(t, stdout, tt.want...)
		})
	}
}

func TestCLI_ReadBytesSanitized(t *testing.T) {
	h := newTestHandler()
	// "AB" ESC "[2J" NUL ... : terminal escape sequences from the device
	copy(h.holding[0:], []uint16{0x4142, 0x1b5b, 0x324a, 0x0007, 0x4344, 0x4546, 0x4748, 0x494a, 0x4b4c})
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "rh:bytes:0+16")
	if strings.ContainsAny(stdout, "\x1b\x07\x00") {
		t.Fatalf("control characters printed raw: %q", stdout)
	}
	expectLines(t, stdout,
		"0x0000\t0     : 41421b5b324a0007 434445464748494a <AB.[2J..CDEFGHIJ>",
		"0x0008\t8     : 4b <K>",
	)
	if !strings.Contains(stdout, "4b <K>") {
		t.Fatal(stdout)
	}
}

func TestCLI_Writes(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)

	stdout, stderr := expectRun(t, 0, "--target", target,
		"wc:5:true",
		"wr:uint16:0:0xabcd",
		"wr:int16:1:-2",
		"wr:uint32:2:0x12345678",
		"wr:int32:4:-1",
		"wr:float32:6:1.5",
		"wr:uint64:8:0x0102030405060708",
		"wr:int64:12:-2",
		"wr:float64:16:-1.5",
		"wr:bytes:20:0xa1b2c3",
		"wr:string:22:hi",
	)
	if stderr != "" {
		t.Errorf("unexpected stderr: %q", stderr)
	}
	expectLines(t, stdout,
		"wrote true at coil address 0x0005",
		"wrote 43981 at register address 0x0000",
		"wrote -2 at register address 0x0001",
		"wrote 305419896 at address 0x0002",
		"wrote -1 at address 0x0004",
		"wrote 1.500000 at address 0x0006",
		"wrote 72623859790382856 at address 0x0008",
		"wrote -2 at address 0x000c",
		"wrote -1.500000 at address 0x0010",
		"wrote 3 bytes at address 0x0014",
		"wrote 2 bytes at address 0x0016",
	)

	if !h.coils[5] {
		t.Error("coil 5 not set")
	}
	want := []uint16{
		0xabcd, 0xfffe, 0x1234, 0x5678, 0xffff, 0xffff, 0x3fc0, 0x0000,
		0x0102, 0x0304, 0x0506, 0x0708, 0xffff, 0xffff, 0xffff, 0xfffe,
		0xbff8, 0, 0, 0, 0xa1b2, 0xc300, 0x6869,
	}
	if got := h.holdingRegs(0, len(want)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("registers:\n got %04x\nwant %04x", got, want)
	}
}

func TestCLI_WritesHonourLayout(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)

	// --endianness used to be ignored for 16-bit writes, and broke every
	// 64-bit write ("unknown codec")
	expectRun(t, 0, "--target", target, "--endianness", "little", "--word-order", "lf",
		"wr:uint16:0:0x1234", "wr:int16:1:-2", "wr:uint32:2:0x01020304", "wr:uint64:4:0x0102030405060708")
	want := []uint16{0x3412, 0xfeff, 0x0403, 0x0201, 0x0807, 0x0605, 0x0403, 0x0201}
	if got := h.holdingRegs(0, len(want)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("registers:\n got %04x\nwant %04x", got, want)
	}

	expectRun(t, 0, "--target", target, "--endianness", "little", "wr:float64:8:1")
	want = []uint16{0xf03f, 0, 0, 0}
	if got := h.holdingRegs(8, 4); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("registers:\n got %04x\nwant %04x", got, want)
	}
}

func TestCLI_ExceptionsAndFailFast(t *testing.T) {
	h := newTestHandler()
	h.limit = 0x100
	target := startServer(t, h)

	failing := []string{
		"rc:0x100", "rh:uint16:0x100", "rh:uint32:0x100", "rh:float32:0x100",
		"rh:uint64:0x100", "rh:float64:0x100", "rh:bytes:0x100",
		"wc:0x100:true", "wr:uint16:0x100:1", "wr:float32:0x100:1", "wr:bytes:0x100:00",
	}
	stdout, stderr := expectRun(t, ExitProtocol, append([]string{"--target", target}, failing...)...)
	// each failure is reported on stdout, the summary on stderr
	if want := fmt.Sprintf("Error: %d of %d operations failed, the first with: ", len(failing), len(failing)); !strings.HasPrefix(stderr, want) ||
		strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "Illegal Data Address") {
		t.Errorf("unexpected stderr %q", stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != len(failing) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(failing), stdout)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "failed to ") || !strings.Contains(l, "Illegal Data Address") {
			t.Errorf("unexpected line %q", l)
		}
	}

	// --fail-fast stops after the first failure
	stdout, _ = expectRun(t, ExitProtocol, "--target", target, "--fail-fast", "rc:0x100", "rc:0")
	expectLines(t, stdout, lines[0])

	// JSON mode reports the same failures as records
	stdout, _ = expectRun(t, ExitProtocol, append([]string{"--target", target, "--json"}, failing...)...)
	recs := decodeJSONLines(t, stdout)
	if len(recs) != len(failing) {
		t.Fatalf("got %d records, want %d", len(recs), len(failing))
	}
	for _, r := range recs {
		if r["addr"] != float64(0x100) || !strings.Contains(fmt.Sprint(r["error"]), "Illegal Data Address") {
			t.Errorf("unexpected record %v", r)
		}
	}
}

func TestCLI_JSON(t *testing.T) {
	h := newTestHandler()
	h.holding[0] = 0xffff
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "--json",
		"rc:0", "rh:int16:0", "rh:uint32:0", "rh:float32:0", "rh:float64:0", "rh:bytes:0+1",
		"wc:0:false", "wr:int32:4:-1", "wr:bytes:8:00ff", "date")
	recs := decodeJSONLines(t, stdout)
	if len(recs) != 10 {
		t.Fatalf("got %d records:\n%s", len(recs), stdout)
	}

	// address 0 used to be dropped by omitempty
	for i, r := range recs[:9] {
		if _, ok := r["addr"]; !ok {
			t.Errorf("record %d has no addr: %v", i, r)
		}
	}
	if recs[0]["op"] != "readBool" || recs[0]["value"] != false {
		t.Errorf("rc record: %v", recs[0])
	}
	if recs[1]["value"] != float64(-1) || recs[1]["hex"] != "0xffff" {
		t.Errorf("int16 record: %v", recs[1])
	}
	if recs[2]["value"] != float64(0xffff0000) || recs[2]["hex"] != "0xffff0000" {
		t.Errorf("uint32 record: %v", recs[2])
	}
	// NaN used to make the encoder fail and the record vanish
	if recs[3]["type"] != "float32" || recs[3]["value"] != "NaN" || recs[3]["hex"] != nil {
		t.Errorf("float32 record: %v", recs[3])
	}
	if recs[4]["type"] != "float64" || recs[4]["value"] != "NaN" {
		t.Errorf("float64 record: %v", recs[4])
	}
	if recs[5]["value"] != "ffff" {
		t.Errorf("bytes record: %v", recs[5])
	}
	if recs[6]["op"] != "writeCoil" || recs[6]["value"] != false {
		t.Errorf("writeCoil record: %v", recs[6])
	}
	if recs[7]["value"] != float64(-1) || recs[7]["type"] != "int32" {
		t.Errorf("write int32 record: %v", recs[7])
	}
	if recs[8]["value"] != "00ff" {
		t.Errorf("write bytes record: %v", recs[8])
	}
	if _, err := time.Parse(time.RFC3339, fmt.Sprint(recs[9]["value"])); err != nil || recs[9]["addr"] != nil {
		t.Errorf("date record: %v", recs[9])
	}
}

func TestCLI_UnitIDAndMisc(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "--unit-id", "9", "rc:0", "suid:0x20", "rc:0", "sleep:1ms", "date")
	if got := h.seenUnitIDs(); !slices.Equal(got, []uint8{9, 32}) {
		t.Errorf("unit IDs seen: %v", got)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if _, err := time.Parse(time.RFC3339, lines[len(lines)-1]); err != nil {
		t.Errorf("date line: %v", err)
	}
}

func TestCLI_Ping(t *testing.T) {
	h := newTestHandler()
	h.failing[2] = true
	h.silent[3] = true
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "ping:2:1ms")
	if !strings.HasPrefix(stdout, "ping: sending 2 requests...\nok: seq = 1") ||
		!strings.Contains(stdout, "2 queries, 2 target replies, 0 transmission errors, 0 timeouts") ||
		!strings.Contains(stdout, "rtt min/avg/max = ") {
		t.Errorf("unexpected output:\n%s", stdout)
	}

	stdout, _ = expectRun(t, 0, "--target", target, "--unit-id", "2", "ping:1")
	if !strings.Contains(stdout, "error (") || !strings.Contains(stdout, "1 transmission errors") {
		t.Errorf("unexpected output:\n%s", stdout)
	}

	stdout, _ = expectRun(t, 0, "--target", target, "--unit-id", "3", "--timeout", "100ms", "ping:1")
	if !strings.Contains(stdout, "timeout (") || !strings.Contains(stdout, "1 timeouts") {
		t.Errorf("unexpected output:\n%s", stdout)
	}

	stdout, _ = expectRun(t, 0, "--target", target, "--json", "--timeout", "100ms", "ping:1", "suid:2", "ping:1", "suid:3", "ping:1")
	recs := decodeJSONLines(t, stdout)
	if len(recs) != 6 {
		t.Fatalf("got %d records:\n%s", len(recs), stdout)
	}
	if recs[1]["op"] != "pingSummary" {
		t.Errorf("summary record: %v", recs[1])
	}
	for i, status := range map[int]string{0: "ok", 2: "error"} {
		if v, _ := recs[i]["value"].(map[string]any); v["status"] != status {
			t.Errorf("record %d: %v", i, recs[i])
		}
	}
}

func TestCLI_PingTimeoutJSON(t *testing.T) {
	h := newTestHandler()
	h.silent[1] = true
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "--json", "--timeout", "100ms", "ping:1")
	recs := decodeJSONLines(t, stdout)
	if v, _ := recs[0]["value"].(map[string]any); v["status"] != "timeout" {
		t.Errorf("unexpected record: %v", recs[0])
	}
}

func TestCLI_UnitIDScan(t *testing.T) {
	h := newTestHandler()
	h.limit = 0 // every read is an illegal data address: device present
	for id := range 256 {
		if id != 1 && id != 7 && id != 9 {
			h.failing[uint8(id)] = true
		}
	}
	h.failing[9] = false
	h.silent[9] = true
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "--timeout", "50ms", "scan:s")
	if !strings.Contains(stdout, "0x01 (  1): ok\n") || !strings.Contains(stdout, "0x07 (  7): ok\n") ||
		!strings.Contains(stdout, "found 2 devices (253 errors, 1 timeouts, 0 gateway timeouts)") {
		t.Errorf("unexpected output (tail):\n%s", stdout[max(0, len(stdout)-400):])
	}

	stdout, _ = expectRun(t, 0, "--target", target, "--timeout", "50ms", "--json", "scan:s")
	recs := decodeJSONLines(t, stdout)
	last := recs[len(recs)-1]
	if last["op"] != "unitIDScanSummary" {
		t.Fatalf("last record: %v", last)
	}
	if v, _ := last["value"].(map[string]any); v["found"] != float64(2) || v["timeouts"] != float64(1) {
		t.Errorf("summary: %v", last)
	}
}

// TestCLI_FullScans runs complete coil and register scans over the whole
// address space against a device with a few valid addresses.
func TestCLI_FullScans(t *testing.T) {
	h := newTestHandler()
	h.limit = 3
	h.coils[1] = true
	h.holding[2] = 0x2a
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "scan:c", "scan:hr")
	expectLines(t, stdout,
		"starting coil scan",
		"0x0000\t0     : false",
		"0x0001\t1     : true",
		"0x0002\t2     : false",
		"found 3 coils",
		"starting holding register scan",
		"0x0000\t0     : 0x0000\t0",
		"0x0001\t1     : 0x0000\t0",
		"0x0002\t2     : 0x002a\t42",
		"found 3 holding registers",
	)
}

// newDirectClient connects a client to target for calling the executor directly.
func newDirectClient(t *testing.T, target string, timeout time.Duration) *modbus.Client {
	t.Helper()
	c, err := modbus.New(modbus.Config{URL: target, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestExecute_ScansStopWhenInterrupted(t *testing.T) {
	for _, arg := range []string{"scan:c", "scan:di", "scan:hr", "scan:ir", "scan:s"} {
		t.Run(arg, func(t *testing.T) {
			h := newTestHandler()
			h.failing[1] = true // every request fails with an error
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.onRequest = func(n int64) {
				if n == 10 {
					cancel()
				}
			}
			target := startServer(t, h)
			client := newDirectClient(t, target, time.Second)

			runList, err := parseOperations([]string{arg})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			_ = executeOperations(ctx, client, runList, 1, modbus.BigEndian, modbus.HighWordFirst, newOutput(&buf, false), false)

			// scanning used to go on through all 65536 addresses after an
			// interrupt, printing a failure for each of them
			if n := h.requests.Load(); n > 12 {
				t.Errorf("%d requests sent after the interrupt", n)
			}
			if lines := strings.Count(buf.String(), "\n"); lines > 13 {
				t.Errorf("%d lines printed:\n%s", lines, buf.String())
			}
		})
	}
}

func TestExecute_RepeatUntilInterrupted(t *testing.T) {
	h := newTestHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.onRequest = func(n int64) {
		if n == 5 {
			cancel()
		}
	}
	target := startServer(t, h)
	client := newDirectClient(t, target, time.Second)

	runList, err := parseOperations([]string{"rc:0", "repeat"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := executeOperations(ctx, client, runList, 1, modbus.BigEndian, modbus.HighWordFirst, newOutput(&buf, true), false); !errors.Is(err, errInterrupted) {
		t.Errorf("an interrupted run should report the interrupt, got %v", err)
	}
	if n := h.requests.Load(); n < 5 {
		t.Errorf("only %d requests sent", n)
	}
}

func TestExecute_InterruptedSleepAndPing(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)
	client := newDirectClient(t, target, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	runList, err := parseOperations([]string{"sleep:1m"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// an interrupt during sleep used to exit with status 0
	if err := executeOperations(ctx, client, runList, 1, modbus.BigEndian, modbus.HighWordFirst, newOutput(&bytes.Buffer{}, false), false); !errors.Is(err, errInterrupted) {
		t.Errorf("an interrupted sleep should report the interrupt, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("sleep not interrupted")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	var buf bytes.Buffer
	performPing(ctx2, client, 1, 1000, time.Minute, newOutput(&buf, false))
	if strings.Contains(buf.String(), "statistics") {
		t.Errorf("unexpected output:\n%s", buf.String())
	}
}

func TestExecute_CancelledBeforeStart(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)
	client := newDirectClient(t, target, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runList, _ := parseOperations([]string{"rc:0"})
	if err := executeOperations(ctx, client, runList, 1, modbus.BigEndian, modbus.HighWordFirst, newOutput(&bytes.Buffer{}, false), false); !errors.Is(err, errInterrupted) {
		t.Errorf("expected the interrupt, got %v", err)
	}
	var buf bytes.Buffer
	performPing(ctx, client, 1, 3, 0, newOutput(&buf, false))
	performUnitIDScan(ctx, client, newOutput(&buf, false))
	if h.requests.Load() != 0 {
		t.Errorf("%d requests sent on a cancelled context", h.requests.Load())
	}
}

func TestRuntimeCodecErrors(t *testing.T) {
	if _, err := runtimeCodec("nonsense/layout:9"); err == nil {
		t.Error("expected an error for an unknown codec")
	}
	if _, err := runtimeCodec("uint32/layout:21"); err == nil {
		t.Error("expected an error for a mismatched layout")
	}
	if _, err := readRuntimeValues(context.Background(), nil, 1, 0, 1, true, 2, "nonsense"); err == nil {
		t.Error("expected an error")
	}
	if err := writeRuntimeValue(context.Background(), nil, 1, 0, uint16(1), "nonsense"); err == nil {
		t.Error("expected an error")
	}
	// value of the wrong type for the codec
	if err := writeRuntimeValue(context.Background(), nil, 1, 0, "x", "uint16/layout:21"); err == nil {
		t.Error("expected an error")
	}

	// the decoding step fails when a codec is wider than the value
	h := newTestHandler()
	target := startServer(t, h)
	client := newDirectClient(t, target, time.Second)
	_, err := readRuntimeValues(context.Background(), client, 1, 0, 1, true, 1, "uint32/layout:4321")
	if err == nil || !strings.Contains(err.Error(), "decode error at 0x0000") {
		t.Errorf("got %v", err)
	}
}

func TestLayoutNames(t *testing.T) {
	for _, e := range []modbus.Endianness{modbus.BigEndian, modbus.LittleEndian} {
		for _, w := range []modbus.WordOrder{modbus.HighWordFirst, modbus.LowWordFirst} {
			for _, c := range []struct {
				name string
				regs uint16
			}{{"uint16", 1}, {"uint32", 2}, {"uint64", 4}} {
				id := c.name + "/layout:" + layoutName(e, w, c.regs)
				if _, err := runtimeCodec(id); err != nil {
					t.Errorf("endianness %v, word order %v: %v", e, w, err)
				}
			}
		}
	}
}

func TestCompleteOperations(t *testing.T) {
	got, _ := completeOperations(nil, nil, "")
	if len(got) == 0 {
		t.Fatal("no completions")
	}
	cmd := newRootCmd()
	for _, flag := range []string{"parity", "endianness", "word-order"} {
		fn, ok := cmd.GetFlagCompletionFunc(flag)
		if !ok {
			t.Fatalf("no completion for --%s", flag)
		}
		if vals, _ := fn(cmd, nil, ""); len(vals) == 0 {
			t.Errorf("empty completion for --%s", flag)
		}
	}
}

func TestCLI_JSONNonFiniteFloats(t *testing.T) {
	h := newTestHandler()
	target := startServer(t, h)

	stdout, _ := expectRun(t, 0, "--target", target, "--json",
		"wr:float32:0:NaN", "wr:float64:2:-Inf", "rh:float32:0", "rh:float64:2", "wr:float64:6:+Inf", "rh:float64:6")
	recs := decodeJSONLines(t, stdout)
	want := []string{"NaN", "-Inf", "NaN", "-Inf", "+Inf", "+Inf"}
	if len(recs) != len(want) {
		t.Fatalf("got %d records, want %d:\n%s", len(recs), len(want), stdout)
	}
	for i, w := range want {
		if recs[i]["value"] != w {
			t.Errorf("record %d: %v, want value %q", i, recs[i], w)
		}
	}
}
