package agent

import (
	"syscall"
	"unsafe"
)

// unixSiginfo is large enough for the kernel's siginfo_t.
type unixSiginfo [128]byte

const (
	pWait   = 1          // P_PID
	wExited = 0x4        // WEXITED
	wNowait = 0x01000000 // WNOWAIT
)

// waitNoReap blocks until pid exits without reaping it, so os/exec can
// still collect the status.
func waitNoReap(pid int, info *unixSiginfo) error {
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, pWait, uintptr(pid), uintptr(unsafe.Pointer(info)), wExited|wNowait, 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}
