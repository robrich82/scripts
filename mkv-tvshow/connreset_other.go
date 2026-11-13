//go:build !windows

package main

import "syscall"

// errConnReset is the error a read gets when the peer resets the connection.
var errConnReset error = syscall.ECONNRESET
