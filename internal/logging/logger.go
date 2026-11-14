// SPDX-License-Identifier: MIT

package logging

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"runtime/debug"
)

// Logger is the logging interface used by internal packages.
// The public modbus package re-exports this as modbus.Logger.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// NewStdLogger wraps a stdlib *log.Logger so it satisfies the Logger interface.
// If l is nil, output is written to os.Stdout with no flags.
func NewStdLogger(l *log.Logger) Logger {
	if l == nil {
		l = log.New(os.Stdout, "", 0)
	}
	return &stdLogger{l: l}
}

type stdLogger struct{ l *log.Logger }

func (sl *stdLogger) Debugf(format string, args ...any) { sl.l.Printf(format, args...) }
func (sl *stdLogger) Infof(format string, args ...any)  { sl.l.Printf(format, args...) }
func (sl *stdLogger) Warnf(format string, args ...any)  { sl.l.Printf(format, args...) }
func (sl *stdLogger) Errorf(format string, args ...any) { sl.l.Printf(format, args...) }

// NewSlogLogger wraps a slog.Handler so it satisfies the Logger interface.
// Use slog.NewJSONHandler, slog.NewTextHandler, or any third-party handler.
func NewSlogLogger(h slog.Handler) Logger {
	if h == nil {
		return NopLogger()
	}
	return &slogLogger{sl: slog.New(h)}
}

type slogLogger struct{ sl *slog.Logger }

func (ll *slogLogger) Debugf(format string, args ...any) {
	ll.sl.DebugContext(context.Background(), fmt.Sprintf(format, args...))
}

func (ll *slogLogger) Infof(format string, args ...any) {
	ll.sl.InfoContext(context.Background(), fmt.Sprintf(format, args...))
}

func (ll *slogLogger) Warnf(format string, args ...any) {
	ll.sl.WarnContext(context.Background(), fmt.Sprintf(format, args...))
}

func (ll *slogLogger) Errorf(format string, args ...any) {
	ll.sl.ErrorContext(context.Background(), fmt.Sprintf(format, args...))
}

// FieldLogger extends Logger with structured key-value logging. If a Logger
// also implements FieldLogger, the library uses the structured methods for
// internal log entries, providing richer observability with tools like slog,
// zap, or zerolog.
type FieldLogger interface {
	Logger

	// With returns a new FieldLogger with the given key-value pairs pre-set.
	// Keys should be strings; values can be any type.
	With(keysAndValues ...any) FieldLogger

	// DebugKV logs at debug level with structured key-value pairs.
	DebugKV(msg string, keysAndValues ...any)

	// InfoKV logs at info level with structured key-value pairs.
	InfoKV(msg string, keysAndValues ...any)

	// WarnKV logs at warn level with structured key-value pairs.
	WarnKV(msg string, keysAndValues ...any)

	// ErrorKV logs at error level with structured key-value pairs.
	ErrorKV(msg string, keysAndValues ...any)
}

// NewSlogFieldLogger wraps a slog.Handler as a FieldLogger with structured
// key-value support. Use with slog.NewJSONHandler or slog.NewTextHandler for
// rich structured output.
func NewSlogFieldLogger(h slog.Handler) FieldLogger {
	if h == nil {
		return &nopFieldLogger{}
	}
	return &slogFieldLogger{sl: slog.New(h)}
}

type slogFieldLogger struct{ sl *slog.Logger }

func (sf *slogFieldLogger) Debugf(format string, args ...any) {
	sf.sl.DebugContext(context.Background(), fmt.Sprintf(format, args...))
}
func (sf *slogFieldLogger) Infof(format string, args ...any) {
	sf.sl.InfoContext(context.Background(), fmt.Sprintf(format, args...))
}
func (sf *slogFieldLogger) Warnf(format string, args ...any) {
	sf.sl.WarnContext(context.Background(), fmt.Sprintf(format, args...))
}
func (sf *slogFieldLogger) Errorf(format string, args ...any) {
	sf.sl.ErrorContext(context.Background(), fmt.Sprintf(format, args...))
}

func (sf *slogFieldLogger) With(keysAndValues ...any) FieldLogger {
	return &slogFieldLogger{sl: sf.sl.With(keysAndValues...)}
}

func (sf *slogFieldLogger) DebugKV(msg string, keysAndValues ...any) {
	sf.sl.DebugContext(context.Background(), msg, keysAndValues...)
}
func (sf *slogFieldLogger) InfoKV(msg string, keysAndValues ...any) {
	sf.sl.InfoContext(context.Background(), msg, keysAndValues...)
}
func (sf *slogFieldLogger) WarnKV(msg string, keysAndValues ...any) {
	sf.sl.WarnContext(context.Background(), msg, keysAndValues...)
}
func (sf *slogFieldLogger) ErrorKV(msg string, keysAndValues ...any) {
	sf.sl.ErrorContext(context.Background(), msg, keysAndValues...)
}

// ContextLogger extends Logger with context-aware logging methods. If a Logger
// also implements ContextLogger, the library can propagate trace/span context
// and request-scoped fields through log entries.
type ContextLogger interface {
	Logger
	DebugContext(ctx context.Context, msg string, keysAndValues ...any)
	InfoContext(ctx context.Context, msg string, keysAndValues ...any)
	WarnContext(ctx context.Context, msg string, keysAndValues ...any)
	ErrorContext(ctx context.Context, msg string, keysAndValues ...any)
}

func (sf *slogFieldLogger) DebugContext(ctx context.Context, msg string, keysAndValues ...any) {
	sf.sl.DebugContext(ctx, msg, keysAndValues...)
}
func (sf *slogFieldLogger) InfoContext(ctx context.Context, msg string, keysAndValues ...any) {
	sf.sl.InfoContext(ctx, msg, keysAndValues...)
}
func (sf *slogFieldLogger) WarnContext(ctx context.Context, msg string, keysAndValues ...any) {
	sf.sl.WarnContext(ctx, msg, keysAndValues...)
}
func (sf *slogFieldLogger) ErrorContext(ctx context.Context, msg string, keysAndValues ...any) {
	sf.sl.ErrorContext(ctx, msg, keysAndValues...)
}

// NopLogger returns a Logger that discards all log output.
// Useful in tests or when logging is intentionally disabled.
func NopLogger() Logger {
	return &nopLogger{}
}

type nopLogger struct{}

func (*nopLogger) Debugf(string, ...any) {}
func (*nopLogger) Infof(string, ...any)  {}
func (*nopLogger) Warnf(string, ...any)  {}
func (*nopLogger) Errorf(string, ...any) {}

type nopFieldLogger struct{ nopLogger }

func (*nopFieldLogger) With(keysAndValues ...any) FieldLogger    { return &nopFieldLogger{} }
func (*nopFieldLogger) DebugKV(msg string, keysAndValues ...any) {}
func (*nopFieldLogger) InfoKV(msg string, keysAndValues ...any)  {}
func (*nopFieldLogger) WarnKV(msg string, keysAndValues ...any)  {}
func (*nopFieldLogger) ErrorKV(msg string, keysAndValues ...any) {}

// PrefixedLogger is the internal prefixing adapter used by transports, client,
// and server. When the inner Logger implements FieldLogger, the component name
// is added as a structured "component" field and level-specific KV methods are
// used. Otherwise the prefix and level are prepended as a formatted string.
type PrefixedLogger struct {
	prefix string
	inner  Logger
	field  FieldLogger // non-nil when inner implements FieldLogger
}

// NewPrefixedLogger creates a prefixing logger. If l is nil, a no-op logger is
// used so that an unconfigured library never produces unexpected output.
//
// l is the application's code. Every call of it is guarded: a call that does
// not return normally loses its entry, and the failure is written once more,
// with the value and the stack, through l's Errorf (see rescue). A FieldLogger
// whose With does not return normally is used through its plain Logger methods.
func NewPrefixedLogger(prefix string, l Logger) *PrefixedLogger {
	if l == nil {
		l = NopLogger()
	}
	pl := &PrefixedLogger{prefix: prefix, inner: l}
	if fl, ok := l.(FieldLogger); ok {
		pl.field = pl.with(fl)
	}
	return pl
}

// with returns fl.With("component", prefix), or nil when that does not return
// normally (or returns nil): the logger then writes formatted lines.
func (l *PrefixedLogger) with(fl FieldLogger) (field FieldLogger) {
	defer l.rescue("FieldLogger.With")
	return fl.With("component", l.prefix)
}

// rescue, deferred by every method that calls the application's logger, ends
// a call that did not return normally: the entry is lost, and the failure is
// written as a formatted line at error level, with the value and the stack,
// through the plain Errorf of the logger, which a logger that fails on one
// entry may still take. When that does not return normally either, the
// failure is dropped. The library holds none of its locks when it logs.
func (l *PrefixedLogger) rescue(method string) {
	if r := recover(); r != nil {
		l.loggerFailed(method, r)
	}
}

func (l *PrefixedLogger) loggerFailed(method string, r any) {
	defer func() { _ = recover() }()
	l.inner.Errorf("%s [error]: Logger.%s did not return normally: %v\n%s", l.prefix, method, r, debug.Stack())
}

func (l *PrefixedLogger) Debug(msg string) {
	if l.field != nil {
		defer l.rescue("DebugKV")
		l.field.DebugKV(msg)
		return
	}
	defer l.rescue("Debugf")
	l.inner.Debugf("%s [debug]: %s", l.prefix, msg)
}

func (l *PrefixedLogger) Debugf(format string, args ...any) {
	if l.field != nil {
		defer l.rescue("DebugKV")
		l.field.DebugKV(fmt.Sprintf(format, args...))
		return
	}
	defer l.rescue("Debugf")
	l.inner.Debugf("%s [debug]: "+format, logPrepend(l.prefix, args)...)
}

func (l *PrefixedLogger) Info(msg string) {
	if l.field != nil {
		defer l.rescue("InfoKV")
		l.field.InfoKV(msg)
		return
	}
	defer l.rescue("Infof")
	l.inner.Infof("%s [info]: %s", l.prefix, msg)
}

func (l *PrefixedLogger) Infof(format string, args ...any) {
	if l.field != nil {
		defer l.rescue("InfoKV")
		l.field.InfoKV(fmt.Sprintf(format, args...))
		return
	}
	defer l.rescue("Infof")
	l.inner.Infof("%s [info]: "+format, logPrepend(l.prefix, args)...)
}

func (l *PrefixedLogger) Warnf(format string, args ...any) {
	if l.field != nil {
		defer l.rescue("WarnKV")
		l.field.WarnKV(fmt.Sprintf(format, args...))
		return
	}
	defer l.rescue("Warnf")
	l.inner.Warnf("%s [warn]: "+format, logPrepend(l.prefix, args)...)
}

func (l *PrefixedLogger) Warning(msg string) {
	if l.field != nil {
		defer l.rescue("WarnKV")
		l.field.WarnKV(msg)
		return
	}
	defer l.rescue("Warnf")
	l.inner.Warnf("%s [warn]: %s", l.prefix, msg)
}

func (l *PrefixedLogger) Warningf(format string, args ...any) {
	if l.field != nil {
		defer l.rescue("WarnKV")
		l.field.WarnKV(fmt.Sprintf(format, args...))
		return
	}
	defer l.rescue("Warnf")
	l.inner.Warnf("%s [warn]: "+format, logPrepend(l.prefix, args)...)
}

func (l *PrefixedLogger) Error(msg string) {
	if l.field != nil {
		defer l.rescue("ErrorKV")
		l.field.ErrorKV(msg)
		return
	}
	defer l.rescue("Errorf")
	l.inner.Errorf("%s [error]: %s", l.prefix, msg)
}

func (l *PrefixedLogger) Errorf(format string, args ...any) {
	if l.field != nil {
		defer l.rescue("ErrorKV")
		l.field.ErrorKV(fmt.Sprintf(format, args...))
		return
	}
	defer l.rescue("Errorf")
	l.inner.Errorf("%s [error]: "+format, logPrepend(l.prefix, args)...)
}

func (l *PrefixedLogger) Fatal(msg string) {
	l.Error(msg)
}

func (l *PrefixedLogger) Fatalf(format string, args ...any) {
	l.Errorf(format, args...)
}

// logPrepend inserts v at the front of args, returning a new slice.
func logPrepend(v any, args []any) []any {
	return append([]any{v}, args...)
}
