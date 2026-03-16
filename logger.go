package modbus

import (
	"log"
	"log/slog"

	intlog "github.com/otfabric/go-modbus/internal/logging"
)

// Logger is the logging interface accepted by Config and ServerConfig.
// Implement this interface to integrate any structured or levelled logging library
// (e.g. zap, zerolog, slog, logrus).
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// NewStdLogger wraps a stdlib *log.Logger so it satisfies the Logger interface.
// If l is nil, output is written to os.Stdout with no flags.
func NewStdLogger(l *log.Logger) Logger {
	return intlog.NewStdLogger(l)
}

// NewSlogLogger wraps a slog.Handler so it satisfies the Logger interface.
// Use slog.NewJSONHandler, slog.NewTextHandler, or any third-party handler.
func NewSlogLogger(h slog.Handler) Logger {
	return intlog.NewSlogLogger(h)
}

// NopLogger returns a Logger that discards all log output.
// Useful in tests or when logging is intentionally disabled.
func NopLogger() Logger {
	return intlog.NopLogger()
}

// FieldLogger extends Logger with structured key-value logging. If the value
// assigned to Config.Logger also implements FieldLogger, the library uses the
// structured methods for internal log entries, providing richer observability
// with tools like slog, zap, or zerolog.
//
// When a FieldLogger is provided, PrefixedLogger automatically adds the component
// name as a "component" structured field instead of string-prepending it.
type FieldLogger = intlog.FieldLogger

// NewSlogFieldLogger wraps a slog.Handler as a FieldLogger with full structured
// key-value support. Use with slog.NewJSONHandler or slog.NewTextHandler for
// rich structured output (e.g. JSON logs with "component", "unit", "fc" fields).
// The returned value also implements ContextLogger.
func NewSlogFieldLogger(h slog.Handler) FieldLogger {
	return intlog.NewSlogFieldLogger(h)
}

// ContextLogger extends Logger with context-aware logging methods. If the
// Logger assigned to Config.Logger also implements ContextLogger, the library
// can propagate trace/span context and request-scoped fields through log
// entries. NewSlogFieldLogger returns a logger that implements ContextLogger.
type ContextLogger = intlog.ContextLogger

// logger is the internal prefixing adapter used by transports, client, and server.
// It wraps the internal logging.PrefixedLogger; kept as a type alias for compatibility.
type logger = intlog.PrefixedLogger

// newLogger creates a prefixing logger for internal use.
func newLogger(prefix string, l Logger) *logger {
	return intlog.NewPrefixedLogger(prefix, l)
}
