//go:build !windows

package main

import "fmt"

func runElevated(file, params string) error {
	return fmt.Errorf("elevation not supported on this platform")
}
