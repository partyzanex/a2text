package clid

import (
	"errors"
	"fmt"
	"net"
)

// errNonLoopbackBind is returned by requireLoopbackAddr when the
// operator-supplied address would have made the listener reachable
// from outside the loopback interface. Defense-in-depth: mTLS still
// gates handshakes, but an exposed gRPC / pprof port is a DoS and
// reconnaissance surface we refuse on principle.
var errNonLoopbackBind = errors.New("clid: bind address must be on the loopback interface")

// requireLoopbackAddr parses a "host:port" string and rejects it if
// the host is not a literal loopback IP (127.0.0.0/8 or ::1). The
// empty host (":port") is rejected because Listen would interpret
// it as "all interfaces". Hostnames are rejected too — DNS lookup
// can resolve "localhost" to non-loopback in unusual /etc/hosts
// setups, so we require the operator to spell out the IP.
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("clid: parse bind address %q: %w", addr, err)
	}

	if host == "" {
		return fmt.Errorf("%w: empty host in %q binds all interfaces", errNonLoopbackBind, addr)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP literal (use 127.0.0.1 or ::1)", errNonLoopbackBind, host)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q is not a loopback address", errNonLoopbackBind, host)
	}

	return nil
}
