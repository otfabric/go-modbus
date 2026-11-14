// SPDX-License-Identifier: MIT

package callback

import (
	"errors"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	errCall := errors.New("call failed")
	for _, tc := range []struct {
		name     string
		fn       func() error
		wantErr  error
		wantFail bool
	}{
		{"returns nil", func() error { return nil }, nil, false},
		{"returns an error", func() error { return errCall }, errCall, false},
		{"does not return normally", func() error { panic("boom") }, nil, true},
		{"does not return normally with an error", func() error { panic(errCall) }, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail, err := Run("App.Method", tc.fn)
			if !tc.wantFail {
				if fail != nil || err != tc.wantErr {
					t.Fatalf("Run = %v, %v; want nil, %v", fail, err, tc.wantErr)
				}
				return
			}
			if fail == nil {
				t.Fatal("no Failure for a call that did not return normally")
			}
			if err != error(fail) {
				t.Errorf("err = %v, want the Failure", err)
			}
			if fail.Name != "App.Method" || fail.Value == nil {
				t.Errorf("Failure %+v", fail)
			}
			if !strings.Contains(string(fail.Stack), "goroutine ") {
				t.Errorf("Failure has no stack: %q", fail.Stack)
			}
			if want := "App.Method did not return normally: "; !strings.HasPrefix(fail.Error(), want) {
				t.Errorf("Error() = %q, want prefix %q", fail.Error(), want)
			}
		})
	}
}
