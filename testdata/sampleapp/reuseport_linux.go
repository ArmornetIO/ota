package main

// SO_REUSEPORT on Linux; the syscall package does not export it there.
const soReusePort = 0xf
