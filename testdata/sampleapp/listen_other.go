//go:build !(linux || darwin)

package main

import "net"

// listenShared falls back to a plain listener where port sharing is not set up
// here; the side-by-side test only runs on Linux.
func listenShared(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
