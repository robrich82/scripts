//go:build windows

package main

import "syscall"

// errConnReset is the error a read gets when the peer resets the connection.
// syscall.ECONNRESET doesn't match it on Windows.
var errConnReset error = syscall.WSAECONNRESET
