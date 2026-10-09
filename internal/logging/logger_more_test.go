// SPDX-License-Identifier: MIT

package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

type ctxKey struct{}

// ctxHandler is a slog.Handler that records the context value it was called with.
type ctxHandler struct {
	slog.Handler
	seen *[]any
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	*h.seen = append(*h.seen, ctx.Value(ctxKey{}))
	return h.Handler.Handle(ctx, r)
}

func TestNewSlogLogger_NilHandler(t *testing.T) {
	l := NewSlogLogger(nil)
	if _, ok := l.(*nopLogger); !ok {
		t.Fatalf("NewSlogLogger(nil) = %T, want the no-op logger", l)
	}
	l.Errorf("discarded %d", 1)
}

func TestNewSlogFieldLogger_NilHandler(t *testing.T) {
	fl := NewSlogFieldLogger(nil)
	if _, ok := fl.(*nopFieldLogger); !ok {
		t.Fatalf("NewSlogFieldLogger(nil) = %T, want the no-op field logger", fl)
	}
	child := fl.With("k", "v")
	if _, ok := child.(*nopFieldLogger); !ok {
		t.Fatalf("With on the no-op field logger = %T, want a no-op field logger", child)
	}
	for _, l := range []FieldLogger{fl, child} {
		l.DebugKV("m", "k", 1)
		l.InfoKV("m", "k", 1)
		l.WarnKV("m", "k", 1)
		l.ErrorKV("m", "k", 1)
		l.Debugf("m %d", 1)
		l.Errorf("m %d", 1)
	}
}

func TestSlogFieldLogger_ContextMethods(t *testing.T) {
	var buf bytes.Buffer
	var seen []any
	h := ctxHandler{
		Handler: slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		seen:    &seen,
	}
	cl, ok := NewSlogFieldLogger(h).(ContextLogger)
	if !ok {
		t.Fatal("slog field logger does not implement ContextLogger")
	}
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-42")

	cl.DebugContext(ctx, "ctx-debug", "unit", 1)
	cl.InfoContext(ctx, "ctx-info", "unit", 2)
	cl.WarnContext(ctx, "ctx-warn", "unit", 3)
	cl.ErrorContext(ctx, "ctx-error", "unit", 4)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []struct{ level, msg, kv string }{
		{"level=DEBUG", "msg=ctx-debug", "unit=1"},
		{"level=INFO", "msg=ctx-info", "unit=2"},
		{"level=WARN", "msg=ctx-warn", "unit=3"},
		{"level=ERROR", "msg=ctx-error", "unit=4"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d log lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, w := range want {
		for _, s := range []string{w.level, w.msg, w.kv} {
			if !strings.Contains(lines[i], s) {
				t.Errorf("line %d %q missing %q", i, lines[i], s)
			}
		}
	}
	if len(seen) != 4 {
		t.Fatalf("handler invoked %d times, want 4", len(seen))
	}
	for i, v := range seen {
		if v != "trace-42" {
			t.Errorf("call %d: context not propagated to the handler (got %v)", i, v)
		}
	}
}
