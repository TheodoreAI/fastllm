package config

import (
	"os"
	"testing"
)

// TestMain keeps the trust store out of the real home directory: every test
// in this package sees a throwaway one.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "fastllm-config-home-")
	if err != nil {
		panic(err)
	}
	trustHomeDir = func() (string, error) { return home, nil }
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
