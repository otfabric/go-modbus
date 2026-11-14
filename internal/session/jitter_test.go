// SPDX-License-Identifier: MIT

package session

import (
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"
)

// Tests of ExponentialBackoffConfig.Jitter. The random source is replaced by
// one that returns a fixed point of its range, so every wait is exact: no test
// depends on a random draw.

// fixedDraw returns a random source that answers every draw from [0, n) with
// the point pick(n) of it, and counts the draws.
func fixedDraw(t *testing.T, pick func(n int64) int64) (randN func(int64) int64, draws func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	return func(n int64) int64 {
			if n <= 0 {
				t.Errorf("draw from [0, %d): the range is empty", n)
				return 0
			}
			v := pick(n)
			if v < 0 || v >= n {
				t.Errorf("draw %d is outside [0, %d)", v, n)
			}
			mu.Lock()
			count++
			mu.Unlock()
			return v
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return count
		}
}

// jittered returns the policy NewExponentialBackoff makes of cfg, with randN
// as its random source.
func jittered(cfg ExponentialBackoffConfig, randN func(int64) int64) RetryPolicy {
	p := NewExponentialBackoff(cfg).(*exponentialBackoff)
	p.randN = randN
	return p
}

// Without Jitter (its zero value) the schedule is the one of v1.15: the random
// source is never used and every wait is BaseDelay × 2^attempt, capped.
func TestExponentialBackoff_ZeroJitterKeepsTheSchedule(t *testing.T) {
	never := func(n int64) int64 {
		t.Errorf("a policy without jitter drew from [0, %d)", n)
		return 0
	}
	const base, maxDelay = 100 * time.Millisecond, 30 * time.Second
	policies := map[string]RetryPolicy{
		"NewExponentialBackoff":           jittered(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: maxDelay}, never),
		"NewExponentialBackoff, unknown":  jittered(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: maxDelay, Jitter: Jitter(7)}, never),
		"ExponentialBackoff (no jitter)":  ExponentialBackoff(base, maxDelay, 0),
		"NewExponentialBackoff, defaults": jittered(ExponentialBackoffConfig{}, never),
	}
	for name, p := range policies {
		b, m := base, maxDelay
		if name == "NewExponentialBackoff, defaults" {
			b, m = 100*time.Millisecond, 30*time.Second
		}
		for _, attempt := range []int{0, 1, 2, 8, 9, 37, 64, 1024, 10000, math.MaxInt} {
			retry, got := p.ShouldRetry(attempt, io.EOF)
			if want := backoffDelay(b, m, attempt); !retry || got != want {
				t.Errorf("%s, attempt %d: retry %v after %v, want true after %v", name, attempt, retry, got, want)
			}
		}
	}
}

// With jitter every wait stays within its bounds, [0, d] for JitterFull and
// [d − d/2, d] for JitterEqual, d being the wait without jitter, and so never
// above MaxDelay, whatever the draw. The extremes of the draw give the bounds
// exactly; attempt 10 000 is past the cap.
func TestExponentialBackoff_JitterBounds(t *testing.T) {
	const base, maxDelay = 100 * time.Millisecond, 30 * time.Second
	draws := map[string]func(n int64) int64{
		"lowest":  func(int64) int64 { return 0 },
		"highest": func(n int64) int64 { return n - 1 },
		"middle":  func(n int64) int64 { return n / 2 },
	}
	attempts := []int{0, 1, 2, 3, 8, 9, 10, 37, 63, 64, 1024, 10000, math.MaxInt}
	for _, mode := range []Jitter{JitterFull, JitterEqual} {
		for drawName, pick := range draws {
			randN, count := fixedDraw(t, pick)
			p := jittered(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: maxDelay, Jitter: mode}, randN)
			for _, attempt := range attempts {
				d := backoffDelay(base, maxDelay, attempt)
				lo := time.Duration(0)
				if mode == JitterEqual {
					lo = d - d/2
				}
				retry, got := p.ShouldRetry(attempt, io.EOF)
				if !retry || got < lo || got > d || got > maxDelay {
					t.Errorf("jitter %d, %s draw, attempt %d: retry %v after %v, want true after a wait in [%v, %v]",
						mode, drawName, attempt, retry, got, lo, d)
				}
				var want time.Duration
				switch drawName {
				case "lowest":
					want = lo
				case "highest":
					want = d
				default:
					if mode == JitterFull {
						want = time.Duration((int64(d) + 1) / 2)
					} else {
						want = d - d/2 + time.Duration((int64(d/2)+1)/2)
					}
				}
				if got != want {
					t.Errorf("jitter %d, %s draw, attempt %d: waits %v, want %v", mode, drawName, attempt, got, want)
				}
			}
			if n := count(); n != len(attempts) {
				t.Errorf("jitter %d, %s draw: %d draws for %d retries, want one each", mode, drawName, n, len(attempts))
			}
		}
	}
}

// At attempt 10 000 the wait is at most MaxDelay, and with the highest draw it
// is MaxDelay itself; the policy still stops after MaxAttempts.
func TestExponentialBackoff_JitterAtAttempt10000(t *testing.T) {
	const maxDelay = 2 * time.Second
	for _, mode := range []Jitter{JitterNone, JitterFull, JitterEqual} {
		randN, _ := fixedDraw(t, func(n int64) int64 { return n - 1 })
		p := jittered(ExponentialBackoffConfig{BaseDelay: time.Millisecond, MaxDelay: maxDelay, Jitter: mode}, randN)
		if retry, d := p.ShouldRetry(10000, io.EOF); !retry || d != maxDelay {
			t.Errorf("jitter %d, attempt 10000: retry %v after %v, want true after %v", mode, retry, d, maxDelay)
		}
		limited := jittered(ExponentialBackoffConfig{BaseDelay: time.Millisecond, MaxDelay: maxDelay, MaxAttempts: 3, Jitter: mode}, randN)
		if retry, d := limited.ShouldRetry(3, io.EOF); retry || d != 0 {
			t.Errorf("jitter %d, attempt 3 of 3: retry %v after %v, want no retry", mode, retry, d)
		}
		if retry, _ := limited.ShouldRetry(0, errors.New("unclassified")); retry {
			t.Errorf("jitter %d: an error that is not retryable was retried", mode)
		}
	}
}

// jitterDelay at the edges of its range: no delay, the shortest delays, the
// largest duration (whose upper bound does not fit a draw of d+1).
func TestJitterDelay_Boundaries(t *testing.T) {
	const maxDuration = time.Duration(math.MaxInt64)
	highest := func(n int64) int64 { return n - 1 }
	lowest := func(int64) int64 { return 0 }
	cases := []struct {
		name  string
		mode  Jitter
		d     time.Duration
		randN func(int64) int64
		want  time.Duration
	}{
		{"none", JitterNone, time.Second, nil, time.Second},
		{"full, zero delay", JitterFull, 0, nil, 0},
		{"equal, zero delay", JitterEqual, 0, nil, 0},
		{"full, negative delay", JitterFull, -1, nil, -1},
		{"full, 1 ns, highest", JitterFull, 1, highest, 1},
		{"full, 1 ns, lowest", JitterFull, 1, lowest, 0},
		{"equal, 1 ns, lowest", JitterEqual, 1, lowest, 1},
		{"equal, 1 ns, highest", JitterEqual, 1, highest, 1},
		{"equal, 3 ns, lowest", JitterEqual, 3, lowest, 2},
		{"equal, 3 ns, highest", JitterEqual, 3, highest, 3},
		{"full, largest, highest", JitterFull, maxDuration, highest, maxDuration - 1},
		{"full, largest, lowest", JitterFull, maxDuration, lowest, 0},
		{"equal, largest, highest", JitterEqual, maxDuration, highest, maxDuration},
		{"equal, largest, lowest", JitterEqual, maxDuration, lowest, maxDuration - maxDuration/2},
	}
	for _, tc := range cases {
		randN := tc.randN
		if randN == nil {
			randN = func(n int64) int64 {
				t.Errorf("%s: drew from [0, %d)", tc.name, n)
				return 0
			}
		}
		if got := jitterDelay(tc.mode, tc.d, randN); got != tc.want {
			t.Errorf("%s: jitterDelay(%d, %v) = %v, want %v", tc.name, tc.mode, tc.d, got, tc.want)
		}
	}
}

// The policy NewExponentialBackoff returns, with its own random source, is
// safe for concurrent use and stays within its bounds (checked under -race;
// the bounds hold for every draw, so the test does not depend on one).
func TestExponentialBackoff_JitterConcurrentUse(t *testing.T) {
	const base, maxDelay = time.Millisecond, 50 * time.Millisecond
	for _, mode := range []Jitter{JitterFull, JitterEqual} {
		p := NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: maxDelay, Jitter: mode})
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for attempt := range 200 {
					d := backoffDelay(base, maxDelay, attempt+g)
					lo := time.Duration(0)
					if mode == JitterEqual {
						lo = d - d/2
					}
					if retry, got := p.ShouldRetry(attempt+g, io.EOF); !retry || got < lo || got > d {
						t.Errorf("jitter %d, attempt %d: retry %v after %v, want a wait in [%v, %v]", mode, attempt+g, retry, got, lo, d)
						return
					}
				}
			})
		}
		wg.Wait()
	}
}
