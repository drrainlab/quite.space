package main

import "fmt"

// parsePort turns the --port flag into a number, with 0 meaning "any".
//
// It stayed behind when the lock gate moved out to clients/lockgate: the gate
// takes a port, it does not parse a command line, and a flag-shaped helper
// living in a package the desktop shell imports would be a small lie about
// what that package is for.
func parsePort(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}
