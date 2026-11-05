// SPDX-License-Identifier: MIT

package adu

import "testing"

func TestUint8(t *testing.T) {
	for _, n := range []int{0, 1, 255} {
		if got := Uint8(n); int(got) != n {
			t.Errorf("Uint8(%d) = %d", n, got)
		}
	}
	for _, n := range []int{-1, 256} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Uint8(%d) did not panic", n)
				}
			}()
			Uint8(n)
		}()
	}
}

func TestUint16(t *testing.T) {
	for _, n := range []int{0, 1, 0xFFFF} {
		if got := Uint16(n); int(got) != n {
			t.Errorf("Uint16(%d) = %d", n, got)
		}
	}
	for _, n := range []int{-1, 0x10000} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Uint16(%d) did not panic", n)
				}
			}()
			Uint16(n)
		}()
	}
}
