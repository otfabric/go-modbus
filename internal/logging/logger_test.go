package logging

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestNewStdLogger_NilDefault(t *testing.T) {
	l := NewStdLogger(nil)
	if l == nil {
		t.Fatal("NewStdLogger(nil) should return a non-nil logger")
	}
}

func TestStdLogger_Output(t *testing.T) {
	var buf bytes.Buffer
	l := NewStdLogger(log.New(&buf, "", 0))
	l.Debugf("hello %s", "debug")
	l.Infof("hello %s", "info")
	l.Warnf("hello %s", "warn")
	l.Errorf("hello %s", "error")
	out := buf.String()
	for _, want := range []string{"hello debug", "hello info", "hello warn", "hello error"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestSlogLogger_Output(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	l := NewSlogLogger(h)
	l.Debugf("slog %s", "debug")
	l.Infof("slog %s", "info")
	l.Warnf("slog %s", "warn")
	l.Errorf("slog %s", "error")
	out := buf.String()
	for _, want := range []string{"slog debug", "slog info", "slog warn", "slog error"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestNopLogger(t *testing.T) {
	l := NopLogger()
	l.Debugf("should not panic")
	l.Infof("should not panic")
	l.Warnf("should not panic")
	l.Errorf("should not panic")
}

func TestPrefixedLogger_NilInner(t *testing.T) {
	pl := NewPrefixedLogger("test", nil)
	if pl == nil {
		t.Fatal("NewPrefixedLogger with nil inner should return a non-nil logger")
	}
}

func TestPrefixedLogger_Formatting(t *testing.T) {
	var buf bytes.Buffer
	inner := NewStdLogger(log.New(&buf, "", 0))
	pl := NewPrefixedLogger("modbus-client", inner)
	pl.Debugf("connecting to %s", "host")
	pl.Debug("connected")
	pl.Infof("opened %d", 1)
	pl.Info("ready")
	pl.Warnf("slow: %dms", 500)
	pl.Warning("degraded")
	pl.Warningf("very slow: %dms", 1000)
	pl.Errorf("failed: %v", "timeout")
	pl.Error("fatal")
	pl.Fatal("crash")
	pl.Fatalf("crash: %v", "oom")

	out := buf.String()
	checks := []string{
		"modbus-client [debug]: connecting to host",
		"modbus-client [debug]: connected",
		"modbus-client [info]: opened 1",
		"modbus-client [info]: ready",
		"modbus-client [warn]: slow: 500ms",
		"modbus-client [warn]: degraded",
		"modbus-client [warn]: very slow: 1000ms",
		"modbus-client [error]: failed: timeout",
		"modbus-client [error]: fatal",
		"modbus-client [error]: crash",
		"modbus-client [error]: crash: oom",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot: %s", want, out)
		}
	}
}

func TestLogPrepend(t *testing.T) {
	result := logPrepend("prefix", []any{"a", "b"})
	if len(result) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(result))
	}
	if result[0] != "prefix" {
		t.Errorf("first element should be 'prefix', got %v", result[0])
	}
}

func TestNopLogger_AllMethods(t *testing.T) {
	l := NopLogger()
	l.Debugf("no-op %d", 1)
	l.Infof("no-op %d", 2)
	l.Warnf("no-op %d", 3)
	l.Errorf("no-op %d", 4)
}

func TestSlogFieldLogger_Output(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fl := NewSlogFieldLogger(h)

	fl.Debugf("formatted %s", "debug")
	fl.Infof("formatted %s", "info")
	fl.Warnf("formatted %s", "warn")
	fl.Errorf("formatted %s", "error")

	out := buf.String()
	for _, want := range []string{"formatted debug", "formatted info", "formatted warn", "formatted error"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestSlogFieldLogger_KV(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fl := NewSlogFieldLogger(h)

	fl.DebugKV("kv-debug", "key", "val1")
	fl.InfoKV("kv-info", "key", "val2")
	fl.WarnKV("kv-warn", "key", "val3")
	fl.ErrorKV("kv-error", "key", "val4")

	out := buf.String()
	for _, want := range []string{"kv-debug", "kv-info", "kv-warn", "kv-error", "val1", "val2", "val3", "val4"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestSlogFieldLogger_With(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fl := NewSlogFieldLogger(h)

	child := fl.With("env", "test")
	child.InfoKV("hello")

	out := buf.String()
	if !strings.Contains(out, "env=test") {
		t.Errorf("expected With field in output, got: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected message in output, got: %s", out)
	}
}

func TestPrefixedLogger_WithFieldLogger(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	fl := NewSlogFieldLogger(h)

	pl := NewPrefixedLogger("transport", fl)
	if pl.field == nil {
		t.Fatal("PrefixedLogger should detect FieldLogger and set field")
	}

	pl.Debug("debug-msg")
	pl.Debugf("debugf %d", 42)
	pl.Info("info-msg")
	pl.Infof("infof %d", 43)
	pl.Warnf("warnf %d", 44)
	pl.Warning("warning-msg")
	pl.Warningf("warningf %d", 45)
	pl.Error("error-msg")
	pl.Errorf("errorf %d", 46)

	out := buf.String()
	for _, want := range []string{
		"component=transport",
		"debug-msg", "debugf 42",
		"info-msg", "infof 43",
		"warnf 44", "warning-msg", "warningf 45",
		"error-msg", "errorf 46",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot: %s", want, out)
		}
	}
}
