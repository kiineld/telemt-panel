package proxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var ErrPortReserved = errors.New("proxy: port is reserved by the panel")

// ErrPortConflict means Docker rejected the container's port binding because
// something else already holds it. It is distinct from the advisory check
// CheckPort does below: this is the authoritative, post-hoc signal that
// actually caught a real conflict, surfaced with a message naming the port
// instead of Docker's raw daemon error string.
var ErrPortConflict = errors.New("proxy: port is already bound on the host")

// CheckPort rejects reserved and out-of-range ports, then makes a best-effort
// bind test.
//
// That bind test is only ever advisory, and not just because of the ordinary
// create-time race it was written to acknowledge (a proxy created a
// microsecond later could still lose the race, which is why the database
// also enforces port uniqueness). More fundamentally, this process runs
// inside the panel's own container, in its own network namespace — bind()
// here says nothing about whether the *host's* published port is free.
// Something already listening on the host (nginx, another compose stack,
// another proxy started outside the panel) sails through this check
// undetected; the real conflict only ever surfaces later, when Docker itself
// tries to publish the port during container start. See ErrPortConflict and
// its handling in Service.Create/startContainer, which is what actually
// catches that case and is why a rejected port here still leaves the create
// saga able to roll back cleanly rather than leaving a broken container
// behind.
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

// isDockerPortConflict reports whether err is the Docker daemon's way of
// saying a host port is already bound by something else. The Docker API has
// no typed error for this — the daemon returns a plain string from the
// underlying platform network stack — so this matches the substrings both
// common phrasings share: "Bind for 0.0.0.0:<port> failed: port is already
// allocated" (another container already published it) and the userland-proxy
// variant ending in "address already in use" (a non-Docker process holds it).
func isDockerPortConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already allocated") || strings.Contains(msg, "address already in use")
}

// wrapPortConflict turns a raw Docker start error into ErrPortConflict when
// it recognizes a port conflict, naming the port instead of leaving the raw
// daemon string to reach the operator verbatim (see postCreate/postRecreate
// in the web package, which special-case ErrPortConflict for exactly this
// reason). Any other Start failure passes through unchanged.
func wrapPortConflict(port int, err error) error {
	if !isDockerPortConflict(err) {
		return err
	}
	return fmt.Errorf("%w: port %d: %v", ErrPortConflict, port, err)
}
