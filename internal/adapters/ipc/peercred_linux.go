//go:build linux

package ipc

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// PeerCred carries the kernel-vouched identity of the process on the other
// end of a Unix-domain socket. Filled from SO_PEERCRED, which the kernel
// stamps at connect() time — the client cannot forge it.
type PeerCred struct {
	PID int32
	UID uint32
	GID uint32
}

// readPeerCred returns the (PID, UID, GID) of the process that connected on
// conn. The connection must be a *net.UnixConn obtained from net.Listener
// over a Unix socket; any other transport returns an error so callers fail
// closed rather than skipping the check silently.
func readPeerCred(conn net.Conn) (PeerCred, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerCred{}, fmt.Errorf("ipc: peercred: connection is %T, want *net.UnixConn", conn)
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerCred{}, fmt.Errorf("ipc: peercred: syscall conn: %w", err)
	}

	var (
		ucred   *unix.Ucred
		readErr error
	)

	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, readErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if ctrlErr != nil {
		return PeerCred{}, fmt.Errorf("ipc: peercred: raw control: %w", ctrlErr)
	}

	if readErr != nil {
		return PeerCred{}, fmt.Errorf("ipc: peercred: getsockopt SO_PEERCRED: %w", readErr)
	}

	if ucred == nil {
		return PeerCred{}, errors.New("ipc: peercred: kernel returned nil ucred")
	}

	return PeerCred{PID: ucred.Pid, UID: ucred.Uid, GID: ucred.Gid}, nil
}
