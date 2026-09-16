package ui

import "os"

// isTTY reports whether the interface can take over the terminal. When it cannot, every
// command falls back to the plain text output, which carries the same answer.
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
