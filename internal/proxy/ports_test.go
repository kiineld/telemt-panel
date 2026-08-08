package proxy

import (
	"errors"
	"net"
	"testing"
)

func TestCheckPortReserved(t *testing.T) {
	for _, p := range []int{80, 8443} {
		if err := CheckPort(p, []int{80, 8443}); !errors.Is(err, ErrPortReserved) {
			t.Errorf("CheckPort(%d) error = %v, want ErrPortReserved", p, err)
		}
	}
}

func TestCheckPortOutOfRange(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		if err := CheckPort(p, nil); err == nil {
			t.Errorf("CheckPort(%d) error = nil, want error", p)
		}
	}
}

func TestCheckPortFree(t *testing.T) {
	// Grab an ephemeral port, note it, release it — it is then almost
	// certainly free.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	if err := CheckPort(port, nil); err != nil {
		t.Errorf("CheckPort(%d) on a free port error = %v", port, err)
	}
}

func TestCheckPortInUse(t *testing.T) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if err := CheckPort(port, nil); err == nil {
		t.Fatalf("CheckPort(%d) on a bound port error = nil, want error", port)
	}
}
