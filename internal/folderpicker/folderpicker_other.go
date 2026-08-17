//go:build !windows && !darwin

package folderpicker

import "context"

// Choose has no implementation on this platform — the "Choose folder"
// button in Settings will show ErrUnsupported and the user can type or
// paste a path into the root field directly instead.
func Choose(ctx context.Context) (string, error) {
	return "", ErrUnsupported
}
