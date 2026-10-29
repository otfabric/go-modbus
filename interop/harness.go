//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
// otfabric/modbus-interop v0.2.0 (libmodbus v3.2.0, PyModbus 3.16.1,
// digitalpetri/modbus 2.1.6, NModbus 3.0.83, tokio-modbus 0.17.0).
//
// The images are pinned by the multi-arch index digests of the release
// manifest, so that a run is reproducible. Moving to another release of
// modbus-interop means
// changing these constants, and the Makefile and the interop workflow follow.
const (
	interopRelease           = "v0.2.0"
	defaultLibmodbusImage    = "ghcr.io/otfabric/modbus-interop-libmodbus@sha256:b8b2d7e71908c225995037596e2d55b4700194b63a533ff57b8af074fb2dbf62"
	defaultPymodbusImage     = "ghcr.io/otfabric/modbus-interop-pymodbus@sha256:33b703491abc2f15c229ff433ffbb79eb00e86a8c56c30fb7799c8ed13832f5a"
	defaultDigitalpetriImage = "ghcr.io/otfabric/modbus-interop-digitalpetri@sha256:c9746131c1ed55c3beb8e507925aa01e466278fd2074a69926e6f4797959bf59"
	defaultNmodbusImage      = "ghcr.io/otfabric/modbus-interop-nmodbus@sha256:761f5ce9f0175125ad4262d98e42460fd5235717f44bd8ab7973239f9ed86f80"
	defaultTokiomodbusImage  = "ghcr.io/otfabric/modbus-interop-tokiomodbus@sha256:b317fc4c8e3c834a40fb1316582d5663885e4688aa17ba7eb69dd8a61495a2c8"
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
	out, stderr, exit, err := runOnce(nil, a.image, "print-capabilities")
	if err != nil || exit != 0 {
		t.Fatalf("print-capabilities of %s: exit %d, %v\n%s", a.image, exit, err, stderr)
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
	featServerDiagnostics          = "serverDiagnostics"
	featServerReportServerID       = "serverReportServerId"
	featServerFileRecord           = "serverFileRecord"
	featServerFIFOQueue            = "serverFifoQueue"
	featClientDiagnostics          = "clientDiagnostics"
	featClientReportServerID       = "clientReportServerId"
	featClientReadFileRecord       = "clientReadFileRecord"
	featClientWriteFileRecord      = "clientWriteFileRecord"
	featClientFIFOQueue            = "clientFifoQueue"
	featClientRawAnyResponse       = "clientRawAnyResponse"
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

var (
	entrypointMu sync.Mutex
	entrypoints  = map[string][]string{} // by image
)

// entrypoint returns the entrypoint of an image.
func entrypoint(image string) ([]string, error) {
	entrypointMu.Lock()
	defer entrypointMu.Unlock()
	if ep, ok := entrypoints[image]; ok {
		return ep, nil
	}
	out, errOut, err := docker("image", "inspect", "--format", "{{json .Config.Entrypoint}}", image)
	if err != nil {
		// "docker run" would pull it; so does this.
		if _, pullErr, perr := docker("pull", "-q", image); perr != nil {
			return nil, fmt.Errorf("docker pull %s: %w\n%s", image, perr, pullErr)
		}
		if out, errOut, err = docker("image", "inspect", "--format", "{{json .Config.Entrypoint}}", image); err != nil {
			return nil, fmt.Errorf("docker image inspect %s: %w\n%s", image, err, errOut)
		}
	}
	var ep []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &ep); err != nil || len(ep) == 0 {
		return nil, fmt.Errorf("image %s has no entrypoint (%q)", image, strings.TrimSpace(out))
	}
	entrypoints[image] = ep
	return ep, nil
}

// runOnce runs the image's program with command to completion and returns
// what it wrote to stdout and stderr, and its exit code. err is set only
// when Docker itself fails.
//
// The output does not go through Docker. Under load, Docker loses the
// output of a container that exits within milliseconds: the result line
// of a reference client went missing that way, with the right exit code
// and nothing at all on stderr, seen in CI with the tokio-modbus and
// libmodbus clients. It was missing from the attached streams of
// "docker run" and from the container log alike. So the program's stdout
// and stderr are files in a directory mounted from the host, written by
// the program itself; the container runs detached and they are read once
// it has exited. Every reference image has /bin/sh, which redirects them.
func runOnce(dockerArgs []string, image string, command ...string) (stdout, stderr string, exit int, err error) {
	ep, err := entrypoint(image)
	if err != nil {
		return "", "", 0, err
	}
	dir, err := os.MkdirTemp("", "go-modbus-interop-run-")
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// The images run as an unprivileged user of their own.
	if err := os.Chmod(dir, 0o777); err != nil {
		return "", "", 0, err
	}
	name := fmt.Sprintf("go-modbus-interop-run-%d-%d", os.Getpid(), containerSeq.Add(1))
	args := append([]string{"run", "-d", "--name", name, "-v", dir + ":/out", "--entrypoint", "/bin/sh"}, dockerArgs...)
	args = append(args, image, "-c", `exec "$@" >/out/stdout 2>/out/stderr`, "sh")
	args = append(append(args, ep...), command...)
	defer func() { _, _, _ = docker("rm", "-f", name) }()
	if _, errOut, err := docker(args...); err != nil {
		return "", "", 0, fmt.Errorf("docker run %s: %w\n%s", image, err, errOut)
	}
	code, errOut, err := docker("wait", name)
	if err != nil {
		return "", "", 0, fmt.Errorf("docker wait %s: %w\n%s", name, err, errOut)
	}
	if exit, err = strconv.Atoi(strings.TrimSpace(code)); err != nil {
		return "", "", 0, fmt.Errorf("docker wait %s: exit code %q: %w", name, code, err)
	}
	// The program has exited: what it wrote is in the files.
	out, err := os.ReadFile(filepath.Join(dir, "stdout"))
	if err != nil {
		return "", "", 0, fmt.Errorf("output of %s (exit %d): %w", image, exit, err)
	}
	errb, _ := os.ReadFile(filepath.Join(dir, "stderr"))
	return string(out), string(errb), exit, nil
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
	var (
		port int
		name string
	)
	for attempt := 1; ; attempt++ {
		port = freePort(t, "127.0.0.1")
		name = fmt.Sprintf("go-modbus-interop-%s-%d-%d", a.name, os.Getpid(), containerSeq.Add(1))
		_, stderr, err := docker("run", "-d", "--name", name,
			"-p", fmt.Sprintf("127.0.0.1:%d:%s", port, containerPort), a.image, "server")
		if err == nil {
			break
		}
		// A container that could not publish its port exists but did not start.
		_, _, _ = docker("rm", "-f", name)
		if !portTaken(stderr) || attempt == portAttempts {
			t.Fatalf("start %s server: %v\n%s", a.name, err, stderr)
		}
		t.Logf("port %d was taken before the %s server could publish it; trying another", port, a.name)
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

// requestsThrough returns the request events of the server, in order, up to
// and including the newest one that satisfies last, once it has been
// reported. A container's log lines arrive in order, so every request the
// server handled before that one is included, however late the log is.
func (s *refServer) requestsThrough(t testing.TB, what string, last func(event) bool) []event {
	t.Helper()
	deadline := time.Now().Add(eventTimeout)
	for {
		var reqs []event
		for _, e := range s.events(t) {
			if e.name() == "request" {
				reqs = append(reqs, e)
			}
		}
		for i := len(reqs) - 1; i >= 0; i-- {
			if last(reqs[i]) {
				return reqs[:i+1]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s server did not report the %s; request events: %v", s.adapter.name, what, reqs)
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
		Data        *string `json:"data"`
		SubFunction *int    `json:"subFunction"`
		File        *int    `json:"file"`
		Record      *int    `json:"record"`
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
	command := append(append([]string{"client"}, clientArgs...), clientTimeouts...)
	stdout, stderr, exit, err := runOnce(dockerArgs, a.image, command...)
	if err != nil {
		t.Fatalf("run %s client: %v", a.name, err)
	}
	r := &result{stderr: stderr, exit: exit}
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

// portAttempts is how often a listener is started on a newly picked port
// before giving up. A port is picked by binding and releasing it, so another
// process, or a container being started in parallel, can take it in between.
const portAttempts = 5

// freePort returns a TCP port that is free on host right now. The port must be
// picked on the address that will be bound: a port can be free on loopback and
// taken on another interface, which makes it unusable for 0.0.0.0.
func freePort(t testing.TB, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// portTaken reports whether a failure to listen says that the port is in use.
func portTaken(msg string) bool {
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated")
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
