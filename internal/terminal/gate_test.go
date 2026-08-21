package terminal

import "testing"

func TestGateEnabledDefaultsToConstructorValue(t *testing.T) {
	if g := NewGate(true); !g.Enabled() {
		t.Error("NewGate(true) should start enabled")
	}
	if g := NewGate(false); g.Enabled() {
		t.Error("NewGate(false) should start disabled")
	}
}

func TestGateSetEnabledTakesEffectImmediately(t *testing.T) {
	g := NewGate(false)
	g.SetEnabled(true)
	if !g.Enabled() {
		t.Error("Enabled() should reflect the value just set")
	}
	g.SetEnabled(false)
	if g.Enabled() {
		t.Error("Enabled() should reflect the value just set")
	}
}

func TestGateInjectEnvDefaultsFalse(t *testing.T) {
	g := NewGate(true)
	if g.InjectEnv() {
		t.Error("InjectEnv() should default to false regardless of the enabled constructor arg")
	}
}

func TestGateInjectEnvIsIndependentOfEnabled(t *testing.T) {
	g := NewGate(false)
	g.SetInjectEnv(true)
	if !g.InjectEnv() {
		t.Error("SetInjectEnv(true) should take effect even while the gate itself is disabled")
	}
	if g.Enabled() {
		t.Error("SetInjectEnv must not also flip Enabled")
	}
}

func TestBaseURLHolderDefaultsEmpty(t *testing.T) {
	h := NewBaseURLHolder()
	if got := h.Get(); got != "" {
		t.Errorf("got %q, want empty string before Set is ever called", got)
	}
}

func TestBaseURLHolderSetThenGet(t *testing.T) {
	h := NewBaseURLHolder()
	h.Set("http://127.0.0.1:34115")
	if got := h.Get(); got != "http://127.0.0.1:34115" {
		t.Errorf("got %q, want the value just Set", got)
	}
	h.Set("http://127.0.0.1:9999")
	if got := h.Get(); got != "http://127.0.0.1:9999" {
		t.Errorf("got %q, want the second Set to overwrite the first", got)
	}
}
