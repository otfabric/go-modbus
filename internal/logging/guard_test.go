// SPDX-License-Identifier: MIT

package logging

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// unruly is a Logger and FieldLogger whose methods named in fail do not
// return normally; the others record their line.
type unruly struct {
	mu    sync.Mutex
	fail  map[string]bool
	lines []string
}

func (u *unruly) do(method, line string) {
	u.mu.Lock()
	fail := u.fail[method]
	if !fail {
		u.lines = append(u.lines, method+" "+line)
	}
	u.mu.Unlock()
	if fail {
		panic(method + " failed")
	}
}

func (u *unruly) Debugf(f string, a ...any) { u.do("Debugf", fmt.Sprintf(f, a...)) }
func (u *unruly) Infof(f string, a ...any)  { u.do("Infof", fmt.Sprintf(f, a...)) }
func (u *unruly) Warnf(f string, a ...any)  { u.do("Warnf", fmt.Sprintf(f, a...)) }
func (u *unruly) Errorf(f string, a ...any) { u.do("Errorf", fmt.Sprintf(f, a...)) }

func (u *unruly) snapshot() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.lines...)
}

// unrulyFields is unruly with the FieldLogger methods.
type unrulyFields struct{ *unruly }

func (u unrulyFields) With(...any) FieldLogger {
	u.mu.Lock()
	fail := u.fail["With"]
	u.mu.Unlock()
	if fail {
		panic("With failed")
	}
	return u
}
func (u unrulyFields) DebugKV(msg string, _ ...any) { u.do("DebugKV", msg) }
func (u unrulyFields) InfoKV(msg string, _ ...any)  { u.do("InfoKV", msg) }
func (u unrulyFields) WarnKV(msg string, _ ...any)  { u.do("WarnKV", msg) }
func (u unrulyFields) ErrorKV(msg string, _ ...any) { u.do("ErrorKV", msg) }

// Every PrefixedLogger method guards the call of the application's logger: one
// that does not return normally loses its entry, the failure is reported once
// through the plain Errorf, with the method, the value and the stack, and the
// caller goes on. On v1.15.3 the value reached the caller of the method.
func TestPrefixedLogger_CallThatDoesNotReturnNormally(t *testing.T) {
	calls := []struct {
		name  string
		call  func(l *PrefixedLogger)
		plain string // the method of Logger it calls on the formatted path
		field string // the method of FieldLogger on the structured path
	}{
		{"Debug", func(l *PrefixedLogger) { l.Debug("m") }, "Debugf", "DebugKV"},
		{"Debugf", func(l *PrefixedLogger) { l.Debugf("m %d", 1) }, "Debugf", "DebugKV"},
		{"Info", func(l *PrefixedLogger) { l.Info("m") }, "Infof", "InfoKV"},
		{"Infof", func(l *PrefixedLogger) { l.Infof("m %d", 1) }, "Infof", "InfoKV"},
		{"Warnf", func(l *PrefixedLogger) { l.Warnf("m %d", 1) }, "Warnf", "WarnKV"},
		{"Warning", func(l *PrefixedLogger) { l.Warning("m") }, "Warnf", "WarnKV"},
		{"Warningf", func(l *PrefixedLogger) { l.Warningf("m %d", 1) }, "Warnf", "WarnKV"},
		{"Error", func(l *PrefixedLogger) { l.Error("m") }, "Errorf", "ErrorKV"},
		{"Errorf", func(l *PrefixedLogger) { l.Errorf("m %d", 1) }, "Errorf", "ErrorKV"},
	}
	for _, c := range calls {
		for _, structured := range []bool{false, true} {
			method := c.plain
			if structured {
				method = c.field
			}
			t.Run(c.name+"/"+method, func(t *testing.T) {
				u := &unruly{fail: map[string]bool{method: true}}
				var inner Logger = u
				if structured {
					inner = unrulyFields{u}
				}
				l := NewPrefixedLogger("p", inner)
				returned := false
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s did not return normally: %v", c.name, r)
						}
					}()
					c.call(l)
					returned = true
				}()
				if !returned {
					return
				}
				lines := u.snapshot()
				want := "Logger." + method + " did not return normally: " + method + " failed"
				if method == "Errorf" {
					// The report goes through Errorf too, which fails again:
					// it is dropped.
					if len(lines) != 0 {
						t.Errorf("lines %q, want none", lines)
					}
					return
				}
				if len(lines) != 1 || !strings.HasPrefix(lines[0], "Errorf p [error]: "+want) || !strings.Contains(lines[0], "goroutine ") {
					t.Errorf("%d lines, the first %q; want one Errorf line %q with a stack", len(lines), firstLine(lines), want)
				}
			})
		}
	}
}

// A FieldLogger whose With does not return normally is used through its plain
// Logger methods, and the failure is reported.
func TestPrefixedLogger_WithThatDoesNotReturnNormally(t *testing.T) {
	u := &unruly{fail: map[string]bool{"With": true}}
	l := NewPrefixedLogger("p", unrulyFields{u})
	if l.field != nil {
		t.Fatal("the structured path is used although With failed")
	}
	l.Infof("after %s", "With")
	lines := u.snapshot()
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "Errorf p [error]: Logger.FieldLogger.With did not return normally: With failed") ||
		lines[1] != "Infof p [info]: after With" {
		t.Errorf("lines %q, want the With failure, then the entry on the formatted path", lines)
	}
}

// A logger that works is used as before: one call, the same line, nothing more.
func TestPrefixedLogger_GuardKeepsTheEntries(t *testing.T) {
	u := &unruly{}
	l := NewPrefixedLogger("p", u)
	l.Debugf("a %d", 1)
	l.Warning("b")
	if got := u.snapshot(); len(got) != 2 || got[0] != "Debugf p [debug]: a 1" || got[1] != "Warnf p [warn]: b" {
		t.Errorf("lines %q", got)
	}
}

// firstLine returns the first line of the first entry, without its stack.
func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	first, _, _ := strings.Cut(lines[0], "\n")
	return first
}
