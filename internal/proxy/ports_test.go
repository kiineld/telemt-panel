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

// TestWrapPortConflictRecognizesDockerMessages covers Finding 5: Docker has
// no typed error for a port conflict, only a raw daemon string, in either of
// two real phrasings depending on whether another container or a bare
// process on the host holds the port.
func TestWrapPortConflictRecognizesDockerMessages(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"another container", errors.New(`Bind for 0.0.0.0:443 failed: port is already allocated`), true},
		{"userland proxy / bare process", errors.New(`driver failed programming external connectivity on endpoint x: listen tcp4 0.0.0.0:443: bind: address already in use`), true},
		{"unrelated docker error", errors.New("no such image"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapPortConflict(443, c.err)
			isConflict := errors.Is(got, ErrPortConflict)
			if isConflict != c.wantErr {
				t.Errorf("wrapPortConflict(443, %v) = %v, errors.Is(_, ErrPortConflict) = %v, want %v", c.err, got, isConflict, c.wantErr)
			}
			if c.wantErr && got.Error() == "" {
				t.Error("wrapped error should not be empty")
			}
			if !c.wantErr && got != c.err {
				t.Errorf("wrapPortConflict should pass through a non-conflict error unchanged, got %v want %v", got, c.err)
			}
		})
	}
}
