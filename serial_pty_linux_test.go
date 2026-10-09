//go:build linux

// SPDX-License-Identifier: MIT

package modbus

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ptySlavePath unlocks the pseudo-terminal behind master and returns the path
// of its slave side.
func ptySlavePath(master *os.File) (string, error) {
	var lock uint32
	if err := ptyIoctl(master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&lock))); err != nil {
		return "", err
	}
	var n uint32
	if err := ptyIoctl(master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

func ptyIoctl(fd uintptr, req uint, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), arg); errno != 0 {
		return errno
	}
	return nil
}
