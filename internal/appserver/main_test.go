package appserver

import (
	"os"
	"testing"
)

// TestMain keeps audit logs and the config trust store out of the real
// ~/.fastllm while tests run harness turns.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fastllm-appserver-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("FASTLLM_AUDIT_DIR", dir+string(os.PathSeparator)+"audit")
	os.Setenv("FASTLLM_TRUST_STORE", dir+string(os.PathSeparator)+"trusted-configs.json")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
