//go:build !windows

package main

import "os"

const onWindows = false

// enableCodes has nothing to do here: a terminal reads the codes as it is.
func enableCodes(*os.File) bool { return true }
