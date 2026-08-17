//go:build !windows

package folderpicker

import "os/exec"

func hideConsoleWindow(cmd *exec.Cmd) {}
