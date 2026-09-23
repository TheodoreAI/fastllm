package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"fastllm/internal/files"
)

func TestCapabilityManifestJSONRoundTrip(t *testing.T) {
	for _, raw := range []string{`{}`, `{"capabilities":null}`, `{"capabilities":[]}`, `{"capabilities":["read"]}`} {
		var req, decoded RunRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(req.Capabilities, decoded.Capabilities) {
			t.Fatalf("manifest changed during round trip: %s -> %s", raw, encoded)
		}
	}
}

func TestChildCapabilitiesCannotExpandParent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested []string
		want      []string
	}{
		{"inherit", nil, []string{"read", "delegate"}},
		{"empty", []string{}, []string{}},
		{"broader", []string{"read", "write", "network", "commands", "delegate"}, []string{"read", "delegate"}},
		{"aliases", []string{"read", "filesystem_write", "web", "shell", "spawn_agent"}, []string{"read", "delegate"}},
		{"disjoint", []string{"write"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockLLM{}
			r := NewRunner(mock, t.TempDir(), "test")
			defer r.Close()
			parent := RunRequest{WorkingDir: r.DefaultWorkingDir, Model: "test", AllowCommands: true,
				PermissionMode: PermissionAuto, Capabilities: []string{"read", "delegate"}, NetworkPolicy: "none"}
			id, err := r.agents.Spawn(parent, "inspect", "", "", 1, SpawnOptions{Capabilities: tc.requested, NetworkPolicy: "public"})
			if err != nil {
				t.Fatal(err)
			}
			r.agents.mu.RLock()
			record := r.agents.agents[id]
			r.agents.mu.RUnlock()
			select {
			case <-record.done:
			case <-time.After(5 * time.Second):
				t.Fatal("child did not finish")
			}
			if !reflect.DeepEqual(record.Capabilities, tc.want) || record.NetworkPolicy != "none" {
				t.Fatalf("child policy = %v / %q; want %v / none", record.Capabilities, record.NetworkPolicy, tc.want)
			}
			for _, tool := range mock.toolsSeen[0] {
				switch tool.Function.Name {
				case "write_file", "web_fetch", "run_command":
					t.Errorf("child regained %s", tool.Function.Name)
				}
			}
		})
	}
}

func TestAttenuationCopiesManifestsAndPreservesRestrictions(t *testing.T) {
	parent := []string{"read", "delegate"}
	child := attenuateCapabilities(parent, nil)
	parent[0] = "write"
	if child[0] != "read" {
		t.Fatal("child aliases parent's manifest")
	}
	grandchild := attenuateCapabilities(child, []string{"write", "network", "delegate"})
	if !reflect.DeepEqual(grandchild, []string{"delegate"}) {
		t.Fatalf("grandchild escalated: %v", grandchild)
	}
	for _, parentPolicy := range []string{"none", " NONE "} {
		for _, requested := range []string{"", "public", "none"} {
			got, err := attenuateNetworkPolicy(parentPolicy, requested)
			if err != nil || got != "none" {
				t.Fatalf("network attenuation = %q, %v", got, err)
			}
		}
	}
}

func TestRunnerCapabilityToolCatalog(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		caps                          []string
		network                       string
		write, web, command, delegate bool
	}{
		{"defaults", nil, "", true, true, true, true},
		{"empty", []string{}, "", false, false, false, false},
		{"read", []string{"read"}, "", false, false, false, false},
		{"no-network", []string{"write", "commands"}, "", true, false, false, false},
		{"network-policy", nil, "none", true, false, false, true},
		{"no-writes", []string{"network", "commands"}, "", false, true, false, false},
		{"aliases", []string{"filesystem_write", "web", "shell", "spawn_agent"}, "", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockLLM{}
			r := NewRunner(mock, t.TempDir(), "test")
			defer r.Close()
			_, err := r.Run(context.Background(), RunRequest{Task: "inspect", Capabilities: tc.caps, NetworkPolicy: tc.network}, nil)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, tool := range mock.toolsSeen[0] {
				seen[tool.Function.Name] = true
			}
			for name, want := range map[string]bool{"write_file": tc.write, "web_fetch": tc.web, "run_command": tc.command, "spawn_agent": tc.delegate, "read_file": true} {
				if seen[name] != want {
					t.Errorf("%s offered=%v; want %v", name, seen[name], want)
				}
			}
		})
	}
}

func TestDispatcherRejectsForgedRestrictedTools(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"write_file", `{"path":"sentinel","content":"changed"}`},
		{"edit_file", `{"path":"sentinel","search":"original","replace":"changed"}`},
		{"patch_file", `{"path":"sentinel","diff":"@@ -1 +1 @@\n-original\n+changed\n"}`},
		{"spawn_agent", `{"task":"escape"}`},
		{"web_fetch", `{"url":"http://127.0.0.1/"}`},
		{"web_search", `{"query":"escape"}`},
		{"run_command", `{"command":"echo escaped"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "sentinel"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			r := NewRunner(nil, root, "test")
			defer r.Close()
			result := r.executeTool(toolExecutionContext{ctx: context.Background(), request: RunRequest{Capabilities: []string{}},
				workingDir: root, fileReader: files.New(root, true), allowCommands: true}, tc.name, tc.args)
			if !strings.Contains(result.output, "disabled") && !strings.Contains(result.output, "denied") {
				t.Fatalf("tool escaped: %s", result.output)
			}
			content, err := os.ReadFile(filepath.Join(root, "sentinel"))
			if err != nil || string(content) != "original" {
				t.Fatalf("restricted tool mutated file: %q, %v", content, err)
			}
		})
	}
}

func TestRestrictedShellPathsCannotRun(t *testing.T) {
	for _, req := range []RunRequest{
		{NetworkPolicy: "none"}, {Capabilities: []string{"write", "commands"}}, {Capabilities: []string{"network", "commands"}},
	} {
		for _, tc := range []struct{ name, args string }{
			{"run_command", `{"command":"echo escaped"}`},
			{"run_command", `{"command":"echo escaped","background":true}`},
			{"write_file", `{"path":"sentinel","content":"changed","then_run":{"command":"echo escaped"}}`},
		} {
			root := t.TempDir()
			r := NewRunner(nil, root, "test")
			for _, live := range []bool{false, true} {
				result := r.executeTool(toolExecutionContext{ctx: context.Background(), request: req, workingDir: root,
					fileReader: files.New(root, true), allowCommands: true, liveCommandOutput: live}, tc.name, tc.args)
				if !strings.Contains(result.output, "disabled") {
					t.Fatalf("restricted command executed: %s", result.output)
				}
			}
			r.Close()
			if _, err := os.Stat(filepath.Join(root, "sentinel")); !os.IsNotExist(err) {
				t.Fatalf("fused mutation happened: %v", err)
			}
		}
	}
}

func TestRunnerRejectsInvalidNetworkPolicy(t *testing.T) {
	r := NewRunner(&mockLLM{}, t.TempDir(), "test")
	defer r.Close()
	if _, err := r.Run(context.Background(), RunRequest{Task: "inspect", NetworkPolicy: "non"}, nil); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if _, err := r.agents.Spawn(RunRequest{WorkingDir: r.DefaultWorkingDir}, "inspect", "", "", 1, SpawnOptions{NetworkPolicy: "non"}); err == nil {
		t.Fatal("invalid child policy accepted")
	}
}
