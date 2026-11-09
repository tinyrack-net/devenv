package sshclient

import (
	"fmt"
	"net"
	"syscall"
	"testing"
)

func TestTransient(t *testing.T) {
	wrap := func(e error) error { return fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Err: e}) }
	for _, e := range []error{syscall.EHOSTUNREACH, syscall.ECONNREFUSED, syscall.ENETUNREACH} {
		if !transient(wrap(e)) {
			t.Errorf("%v should be transient", e)
		}
	}
	if transient(wrap(syscall.EACCES)) || transient(fmt.Errorf("host key mismatch")) {
		t.Error("permanent errors must not be retried")
	}
}
