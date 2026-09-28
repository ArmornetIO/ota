//go:build linux || darwin

package main

import (
	"context"
	"net"
	"syscall"
)

// listenShared binds with SO_REUSEPORT, so an old and a new version can both
// accept on the port during a side-by-side switch.
func listenShared(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), "tcp", addr)
}
