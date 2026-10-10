//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// adapter is one reference implementation, as a container image that follows
// the modbus-interop container contract.
type adapter struct {
	name  string
	image string
}

// The reference images this version of go-modbus is qualified against:
// otfabric/modbus-interop v0.1.0 (libmodbus v3.2.0, PyModbus 3.15.0,
// digitalpetri/modbus 2.1.6, NModbus 3.0.83, tokio-modbus 0.17.0).
//
// The images are pinned by the multi-arch index digests of the release
// manifest, so that a run is reproducible. Moving to another release of
// modbus-interop means
// changing these constants, and the Makefile and the interop workflow follow.
const (
	interopRelease           = "v0.1.0"
	defaultLibmodbusImage    = "ghcr.io/otfabric/modbus-interop-libmodbus@sha256:d51da912841f7afc78095fb043193601e13c463b847384d5316d1ed37d7d0b6d"
	defaultPymodbusImage     = "ghcr.io/otfabric/modbus-interop-pymodbus@sha256:bae6b6cffd82108ac017e157d8a21d36ef2cbec960d938990eb07a52e765a738"
	defaultDigitalpetriImage = "ghcr.io/otfabric/modbus-interop-digitalpetri@sha256:7d78a87dc88cdca4b09b639aa4ed25c604620984233b157df8ab57fc98c79da6"
	defaultNmodbusImage      = "ghcr.io/otfabric/modbus-interop-nmodbus@sha256:6da5066a5274ece1788d545ee7ca9153e8366767a5d6599f916ca69b16e408fb"
	defaultTokiomodbusImage  = "ghcr.io/otfabric/modbus-interop-tokiomodbus@sha256:23f93e71653ef8fdc62de05826a4f015de5141770402b3c398fabd129b7122e7"
)

// Names of the adapters, where a test has to name one.
const (
	libmodbus    = "libmodbus"
	pymodbus     = "pymodbus"
	digitalpetri = "digitalpetri"
	nmodbus      = "nmodbus"
	tokiomodbus  = "tokiomodbus"
)

var defaultImages = []adapter{
	{libmodbus, defaultLibmodbusImage},
	{pymodbus, defaultPymodbusImage},
	{digitalpetri, defaultDigitalpetriImage},
	{nmodbus, defaultNmodbusImage},
	{tokiomodbus, defaultTokiomodbusImage},
}

// adapters returns the reference implementations to test against. The image
// of each can be overridden, for example to try a local build or a candidate:
//
//	MODBUS_INTEROP_LIBMODBUS_IMAGE, MODBUS_INTEROP_PYMODBUS_IMAGE,
//	MODBUS_INTEROP_DIGITALPETRI_IMAGE, MODBUS_INTEROP_NMODBUS_IMAGE,
//	MODBUS_INTEROP_TOKIOMODBUS_IMAGE
//
// MODBUS_INTEROP_ADAPTERS restricts the run to a comma-separated subset.
//
// The references do not all support the same: a test asks the image what it
// declares (see [adapter.has]) and skips what it lacks.
func adapters(t testing.TB) []adapter {
	t.Helper()
	all := make([]adapter, len(defaultImages))
	for i, a := range defaultImages {
		all[i] = adapter{a.name, getenv("MODBUS_INTEROP_"+strings.ToUpper(a.name)+"_IMAGE", a.image)}
	}
	only := os.Getenv("MODBUS_INTEROP_ADAPTERS")
	if only == "" {
		return all
	}
	var out []adapter
	for _, a := range all {
		for _, want := range strings.Split(only, ",") {
			if strings.TrimSpace(want) == a.name {
				out = append(out, a)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("MODBUS_INTEROP_ADAPTERS=%q selects no adapter", only)
	}
	return out
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// forEachAdapter runs fn as a subtest per reference implementation. The
// subtests of different adapters run in parallel: they share nothing but the
// Docker daemon.
func forEachAdapter(t *testing.T, fn func(t *testing.T, a adapter)) {
	t.Helper()
	for _, a := range adapters(t) {
		t.Run(a.name, func(t *testing.T) {
			t.Parallel()
			fn(t, a)
		})
	}
}

// capabilities is the document "print-capabilities" prints.
type capabilities struct {
	Adapter        string `json:"adapter"`
	AdapterVersion string `json:"adapterVersion"`
	Protocol       string `json:"protocol"`
	Upstream       struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"upstream"`
	Roles struct {
		Server bool `json:"server"`
		Client bool `json:"client"`
	} `json:"roles"`
	Features         map[string]bool `json:"features"`
	ServerFunctions  []int           `json:"serverFunctions"`
	ClientOperations []string        `json:"clientOperations"`
}

var (
	capabilitiesMu   sync.Mutex
	capabilitiesDocs = map[string]*capabilities{} // by image
)

func (a adapter) capabilities(t testing.TB) *capabilities {
	t.Helper()
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	if c, ok := capabilitiesDocs[a.image]; ok {
		return c
	}
	out, stderr, err := docker("run", "--rm", a.image, "print-capabilities")
	if err != nil {
		t.Fatalf("print-capabilities of %s: %v\n%s", a.image, err, stderr)
	}
	caps := &capabilities{}
	if err := json.Unmarshal([]byte(out), caps); err != nil {
		t.Fatalf("capabilities of %s are not JSON: %v", a.image, err)
	}
	capabilitiesDocs[a.image] = caps
	return caps
}

// Features of the capability document that the tests ask for.
const (
	featServerUnitIDFiltering      = "serverUnitIdFiltering"
	featServerQuantityCheck        = "serverQuantityCheck"
	featServerMaskWrite            = "serverMaskWrite"
	featServerReadWriteMultiple    = "serverReadWriteMultiple"
	featServerDeviceIdentification = "serverDeviceIdentification"
	featRequestEvents              = "requestEvents"
	featConnectionEvents           = "connectionEvents"
	featClientMaskWrite            = "clientMaskWrite"
	featClientReadWriteMultiple    = "clientReadWriteMultiple"
	featClientDeviceIdentification = "clientDeviceIdentification"
	featClientRaw                  = "clientRaw"
	featClientRepeat               = "clientRepeat"
)

// has reports whether the image declares a feature. A feature an image does
// not mention is one it does not have.
func (a adapter) has(t testing.TB, feature string) bool {
	t.Helper()
	return a.capabilities(t).Features[feature]
}

func (a adapter) hasAll(t testing.TB, features ...string) bool {
	t.Helper()
	for _, f := range features {
		if !a.has(t, f) {
			return false
		}
	}
	return true
}

// need skips the test when the image does not declare every feature.
func (a adapter) need(t testing.TB, features ...string) {
	t.Helper()
	for _, f := range features {
		if !a.has(t, f) {
			t.Skipf("%s does not declare %s", a.name, f)
		}
	}
}

// serves reports whether the reference server answers a function code with
// something other than exception 1.
func (a adapter) serves(t testing.TB, function int) bool {
	t.Helper()
	for _, f := range a.capabilities(t).ServerFunctions {
		if f == function {
			return true
		}
	}
	return false
}

var containerSeq atomic.Int64

func docker(args ...string) (stdout, stderr string, err error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// The port the reference servers listen on inside their container.
const containerPort = "1502"

// refServer is a running reference server container.
type refServer struct {
	adapter adapter
	name    string
	addr    string // host:port on loopback
}

// startServer runs the reference server of a with the baseline fixture and
// waits until it is ready. Only containers started here are ever removed:
// their names carry the prefix go-modbus-interop- and the pid of the test.
func startServer(t testing.TB, a adapter) *refServer {
	t.Helper()
	port := freePort(t)
	name := fmt.Sprintf("go-modbus-interop-%s-%d-%d", a.name, os.Getpid(), containerSeq.Add(1))
	if _, stderr, err := docker("run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:%s", port, containerPort), a.image, "server"); err != nil {
		t.Fatalf("start %s server: %v\n%s", a.name, err, stderr)
	}
	s := &refServer{adapter: a, name: name, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	t.Cleanup(func() {
		if t.Failed() {
			out, errOut, _ := docker("logs", "--tail", "80", name)
			t.Logf("%s server log:\n%s%s", a.name, out, errOut)
		}
		_, _, _ = docker("rm", "-f", name)
	})
	s.waitReady(t)
	return s
}

// How long a reference server may take to become ready. Generous: a JVM or
// a .NET runtime on a loaded CI runner needs its time.
const readyTimeout = 120 * time.Second

// waitReady waits for the readiness file, which the server writes once the
// fixture is loaded and the listener is up.
func (s *refServer) waitReady(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		if _, _, err := docker("exec", s.name, "test", "-f", "/run/modbus-interop/ready"); err == nil {
			return
		}
		if state, _, _ := docker("inspect", "--format", "{{.State.Status}}", s.name); strings.TrimSpace(state) != "running" {
			t.Fatalf("%s server is %q, not running", s.adapter.name, strings.TrimSpace(state))
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s server not ready after %v", s.adapter.name, readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// event is one line of the server's JSON Lines event stream.
type event map[string]any

func (e event) name() string {
	s, _ := e["event"].(string)
	return s
}

// num returns a numeric field, or -1 when the event does not have it.
func (e event) num(field string) int {
	f, ok := e[field].(float64)
	if !ok {
		return -1
	}
	return int(f)
}

func (s *refServer) events(t testing.TB) []event {
	t.Helper()
	out, _, err := docker("logs", s.name)
	if err != nil {
		t.Fatalf("logs of %s server: %v", s.adapter.name, err)
	}
	var evs []event
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("%s server wrote a stdout line that is not JSON: %q", s.adapter.name, line)
		}
		evs = append(evs, e)
	}
	return evs
}

// How long to wait for something a reference server reports asynchronously.
const eventTimeout = 30 * time.Second

// waitEvents returns the events that satisfy match once there are at least n
// of them, and all events seen. It fails when they do not arrive.
func (s *refServer) waitEvents(t testing.TB, what string, n int, match func(event) bool) []event {
	t.Helper()
	deadline := time.Now().Add(eventTimeout)
	for {
		var found []event
		evs := s.events(t)
		for _, e := range evs {
			if match(e) {
				found = append(found, e)
			}
		}
		if len(found) >= n {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s server reported %d %s event(s), want %d; events: %v", s.adapter.name, len(found), what, n, evs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requestEvents returns the request events of the server, in order.
func (s *refServer) requestEvents(t testing.TB, atLeast int) []event {
	t.Helper()
	return s.waitEvents(t, "request", atLeast, func(e event) bool { return e.name() == "request" })
}

// result is the document a reference client prints: see the container
// contract of modbus-interop.
type result struct {
	SchemaVersion string `json:"schemaVersion"`
	Adapter       string `json:"adapter"`
	Operation     string `json:"operation"`
	OK            bool   `json:"ok"`
	Error         *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Connected bool `json:"connected"`
	UnitID    int  `json:"unitId"`
	Requests  int  `json:"requests"`
	Responses int  `json:"responses"`
	Response  *struct {
		Function        int      `json:"function"`
		Bits            []bool   `json:"bits"`
		Registers       []uint16 `json:"registers"`
		Address         *int     `json:"address"`
		Value           any      `json:"value"`
		Quantity        *int     `json:"quantity"`
		AndMask         *int     `json:"andMask"`
		OrMask          *int     `json:"orMask"`
		ConformityLevel *int     `json:"conformityLevel"`
		Objects         []struct {
			ID    int    `json:"id"`
			Value string `json:"value"`
		} `json:"objects"`
		Data *string `json:"data"`
	} `json:"response"`
	Exception *struct {
		Function int `json:"function"`
		Code     int `json:"code"`
	} `json:"exception"`

	exit   int
	raw    map[string]any
	stderr string
}

// summary states the outcome in one line: what the scenario tables expect.
// n is the number of bits, registers or objects in the response.
func (r *result) summary() string {
	code, exc, fc, n := "-", "-", "-", 0
	if r.Error != nil {
		code = r.Error.Code
	}
	if r.Exception != nil {
		exc = fmt.Sprintf("%d/%d", r.Exception.Function, r.Exception.Code)
	}
	if r.Response != nil {
		fc = strconv.Itoa(r.Response.Function)
		n = len(r.Response.Bits) + len(r.Response.Registers) + len(r.Response.Objects)
	}
	return fmt.Sprintf("exit=%d ok=%v error=%s exc=%s fc=%s n=%d req=%d res=%d",
		r.exit, r.OK, code, exc, fc, n, r.Requests, r.Responses)
}

// normalized returns the document without what legitimately differs between
// two runs: who answered, how long it took and the text for people.
func (r *result) normalized() string {
	doc := map[string]any{}
	for k, v := range r.raw {
		if k != "adapter" && k != "elapsedMs" {
			doc[k] = v
		}
	}
	if e, ok := doc["error"].(map[string]any); ok {
		doc["error"] = map[string]any{"code": e["code"]}
	}
	b, _ := json.MarshalIndent(doc, "", " ")
	return string(b)
}

// Timeouts handed to the reference clients. Generous: the suite must pass on
// a loaded machine.
var clientTimeouts = []string{"--connect-timeout-ms", "20000", "--timeout-ms", "20000"}

// runClient runs one operation of the reference client of a against a
// go-modbus server listening on the host at addr, and returns its result
// document.
func runClient(t testing.TB, a adapter, addr string, args ...string) *result {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	// The client runs in a container and reaches the host through the
	// gateway alias, on Docker Desktop and on Linux alike. The server must
	// listen on all interfaces for that: on Linux the alias is the bridge
	// address, not loopback.
	return execClient(t, a, []string{"--add-host=host.docker.internal:host-gateway"},
		append(args, "--host", "host.docker.internal", "--port", port))
}

// runClientAgainst runs one operation of the reference client of a against
// a reference server. The client joins the network namespace of the server
// container: the server's port is published on the host's loopback only,
// which a container cannot reach on Linux.
func runClientAgainst(t testing.TB, a adapter, srv *refServer, args ...string) *result {
	t.Helper()
	return execClient(t, a, []string{"--network", "container:" + srv.name},
		append(args, "--host", "127.0.0.1", "--port", containerPort))
}

func execClient(t testing.TB, a adapter, dockerArgs, clientArgs []string) *result {
	t.Helper()
	cmd := append([]string{"run", "--rm"}, dockerArgs...)
	cmd = append(append(cmd, a.image, "client"), clientArgs...)
	cmd = append(cmd, clientTimeouts...)
	stdout, stderr, err := docker(cmd...)
	r := &result{stderr: stderr}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.exit = exitErr.ExitCode()
	default:
		t.Fatalf("run %s client: %v", a.name, err)
	}
	line := strings.TrimSpace(stdout)
	if strings.Count(line, "\n") != 0 || line == "" {
		t.Fatalf("%s client %v: want one result line on stdout, got %q (exit %d, stderr %q)",
			a.name, clientArgs, stdout, r.exit, stderr)
	}
	if err := json.Unmarshal([]byte(line), r); err != nil {
		t.Fatalf("%s client result is not JSON: %v\n%s", a.name, err, line)
	}
	if err := json.Unmarshal([]byte(line), &r.raw); err != nil {
		t.Fatal(err)
	}
	return r
}

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// wireLog collects the debug log of a go-modbus client or server, which
// carries every frame sent and received, and prints it when the test fails.
type wireLog struct {
	mu    sync.Mutex
	lines []string
}

func newWireLog(t testing.TB, who string) *wireLog {
	w := &wireLog{}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		lines := w.lines
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		t.Logf("%s log (last %d lines):\n%s", who, len(lines), strings.Join(lines, "\n"))
	})
	return w
}

func (w *wireLog) add(level, format string, args ...any) {
	line := level + " " + fmt.Sprintf(format, args...)
	if len(line) > 400 {
		line = line[:400] + "..."
	}
	w.mu.Lock()
	w.lines = append(w.lines, line)
	w.mu.Unlock()
}

func (w *wireLog) Debugf(format string, args ...any) { w.add("debug", format, args...) }
func (w *wireLog) Infof(format string, args ...any)  { w.add("info ", format, args...) }
func (w *wireLog) Warnf(format string, args ...any)  { w.add("warn ", format, args...) }
func (w *wireLog) Errorf(format string, args ...any) { w.add("error", format, args...) }

// logReference records which reference build a test ran against.
func logReference(t testing.TB, a adapter) {
	t.Helper()
	caps := a.capabilities(t)
	if !caps.Roles.Server || !caps.Roles.Client {
		t.Fatalf("%s does not provide both roles", a.image)
	}
	if caps.Adapter != a.name || caps.Protocol != "modbus-tcp" {
		t.Errorf("%s is adapter %q for %q, want %q for modbus-tcp", a.image, caps.Adapter, caps.Protocol, a.name)
	}
	t.Logf("%s: modbus-interop %s, %s %s (%s)", a.name, caps.AdapterVersion, caps.Upstream.Name, caps.Upstream.Version, a.image)
	for _, d := range defaultImages {
		if a.image == d.image && caps.AdapterVersion != interopRelease {
			t.Errorf("%s is pinned as modbus-interop %s but reports %s", a.name, interopRelease, caps.AdapterVersion)
		}
	}
}

// TestReferences checks that the pinned images are what the documentation
// says this version is qualified against.
func TestReferences(t *testing.T) {
	for _, a := range adapters(t) {
		logReference(t, a)
	}
}
