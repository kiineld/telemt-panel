package proxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

var ErrPortReserved = errors.New("proxy: port is reserved by the panel")

// CheckPort rejects reserved and out-of-range ports, then confirms nothing on
// the host is already bound to it. The bind test is advisory: a proxy created
// a microsecond later could still lose a race, which is why the database also
// enforces port uniqueness.
func CheckPort(port int, reserved []int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("proxy: port %d out of range 1-65535", port)
	}
	for _, r := range reserved {
		if port == r {
			return fmt.Errorf("%w: %d is used by the panel's web server", ErrPortReserved, port)
		}
	}

	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("proxy: port %d is already in use on this host: %w", port, err)
	}
	return l.Close()
}
