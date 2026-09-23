//go:build !windows

// Package elevate relaunches this executable with administrator rights. Only
// the Windows sandbox needs it; elsewhere there is nothing to elevate for.
package elevate

import "errors"

// ErrDeclined reports that administrator approval was declined.
var ErrDeclined = errors.New("administrator approval was declined")

// Elevated reports false: no platform here uses elevation.
func Elevated() bool { return false }

// Run is unsupported where no setup step needs administrator rights.
func Run([]string) (int, error) { return 0, errors.New("elevation is only used on Windows") }
