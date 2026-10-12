// Package testshell selects explicit native commands for cross-platform tests.
package testshell

import "runtime"

func Native(windows, posix string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return posix
}
