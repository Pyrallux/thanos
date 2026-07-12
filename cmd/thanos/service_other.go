//go:build !windows

package main

// isWindowsService always returns false on non-Windows platforms.
func isWindowsService() bool {
	return false
}

// runService is a no-op stub on non-Windows platforms.
func runService() error {
	return nil
}