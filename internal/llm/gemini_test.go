package llm

import (
	"encoding/json"
	"testing"
)

func TestToGeminiRequestTranslatesToolCallRoundTrip(t *testing.T) {
	// ID is built via toGeminiToolCallID exactly as GeminiClient.Chat would
	// construct it from a real API response, not a hand-written literal —
	// the whole point under test is that the packed name+thoughtSignature
	// survives being carried in Message history and comes back out
	// correctly on the next request (both here and in the ThoughtSignature
	// assertion below).
	callID := toGeminiToolCallID("read_file", "opaque-signature-abc123", 0)
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Read foo.txt"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:   callID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"foo.txt"}`},
			}},
		},
		{Role: "tool", ToolCallID: callID, Content: "file contents here"},
	}

	req := toGeminiRequest("gemini-flash-latest", messages, []Tool{
		{Type: "function", Function: ToolFunction{Name: "read_file", Description: "reads a file"}},
	}, "")

	if req.SystemInstruction == nil || len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != "You are a helpful assistant." {
		t.Fatalf("system instruction not translated correctly: %+v", req.SystemInstruction)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 || req.Tools[0].FunctionDeclarations[0].Name != "read_file" {
		t.Fatalf("tools not translated correctly: %+v", req.Tools)
	}
	// system message is consumed into SystemInstruction, not left in Contents.
	if len(req.Contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user/functionResponse), got %d: %+v", len(req.Contents), req.Contents)
	}

	userMsg := req.Contents[0]
	if userMsg.Role != "user" || userMsg.Parts[0].Text != "Read foo.txt" {
		t.Fatalf("user message not translated correctly: %+v", userMsg)
	}

	modelMsg := req.Contents[1]
	if modelMsg.Role != "model" {
		t.Fatalf("assistant message should map to role \"model\", got %q", modelMsg.Role)
	}
	if len(modelMsg.Parts) != 1 || modelMsg.Parts[0].FunctionCall == nil {
		t.Fatalf("expected exactly one functionCall part, got: %+v", modelMsg.Parts)
	}
	if modelMsg.Parts[0].FunctionCall.Name != "read_file" {
		t.Fatalf("got function call name %q", modelMsg.Parts[0].FunctionCall.Name)
	}
	if modelMsg.Parts[0].FunctionCall.Args["path"] != "foo.txt" {
		t.Fatalf("got function call args %+v", modelMsg.Parts[0].FunctionCall.Args)
	}
	if modelMsg.Parts[0].ThoughtSignature != "opaque-signature-abc123" {
		t.Fatalf("thoughtSignature should be echoed back on replay, got %q", modelMsg.Parts[0].ThoughtSignature)
	}

	toolReplyMsg := req.Contents[2]
	if toolReplyMsg.Role != "user" {
		t.Fatalf("tool-result message should map to role \"user\" (functionResponse), got %q", toolReplyMsg.Role)
	}
	if len(toolReplyMsg.Parts) != 1 || toolReplyMsg.Parts[0].FunctionResponse == nil {
		t.Fatalf("expected exactly one functionResponse part, got: %+v", toolReplyMsg.Parts)
	}
	if toolReplyMsg.Parts[0].FunctionResponse.Name != "read_file" {
		t.Fatalf("functionResponse name should be recovered from the matching call ID, got %q", toolReplyMsg.Parts[0].FunctionResponse.Name)
	}
	if toolReplyMsg.Parts[0].FunctionResponse.Response["result"] != "file contents here" {
		t.Fatalf("got functionResponse response %+v", toolReplyMsg.Parts[0].FunctionResponse.Response)
	}
}

func TestGeminiToolCallIDRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		signature string
	}{
		{"read_file", "abc123=="},
		{"write_file", ""},                      // some responses omit thoughtSignature entirely
		{"name_with_underscores", "a/b+c=\x1f"}, // exercises the delimiter byte appearing inside the signature itself
	}
	for _, c := range cases {
		id := toGeminiToolCallID(c.name, c.signature, 0)
		meta := parseGeminiToolCallID(id)
		if meta.Name != c.name {
			t.Errorf("name: got %q, want %q", meta.Name, c.name)
		}
		if meta.ThoughtSignature != c.signature {
			t.Errorf("signature: got %q, want %q", meta.ThoughtSignature, c.signature)
		}
	}
}

func TestParseGeminiToolCallIDHandlesUnpackedIDs(t *testing.T) {
	// A plain, non-packed ID (e.g. hand-constructed by a test, or from a
	// hypothetical future caller) should decode to the zero value rather
	// than error or panic — see parseGeminiToolCallID's doc comment.
	meta := parseGeminiToolCallID("not-a-packed-id")
	if meta != (geminiToolCallMeta{}) {
		t.Fatalf("expected zero value for a non-packed ID, got %+v", meta)
	}
}

func TestToGeminiRequestOmitsThinkingConfigForGemmaModels(t *testing.T) {
	req := toGeminiRequest("gemma-4-31b-it", []Message{{Role: "user", Content: "hi"}}, nil, "high")
	if req.GenerationConfig != nil {
		t.Fatalf("expected no GenerationConfig for a gemma-* model, got %+v", req.GenerationConfig)
	}
}

func TestToGeminiRequestIncludesThinkingConfigForGeminiModels(t *testing.T) {
	req := toGeminiRequest("gemini-flash-latest", []Message{{Role: "user", Content: "hi"}}, nil, "high")
	if req.GenerationConfig == nil || req.GenerationConfig.ThinkingConfig == nil || req.GenerationConfig.ThinkingConfig.ThinkingBudget != 16384 {
		t.Fatalf("expected a thinkingConfig with budget 16384, got %+v", req.GenerationConfig)
	}
}

// TestGeminiResponseParsesFunctionCallParts exercises the response-side
// half of the round trip that toGeminiRequest's tests cover on the
// request side — decoding a raw API-shaped JSON payload (rather than
// calling the network-hitting GeminiClient.Chat directly) into
// geminiResponse, mirroring exactly what GeminiClient.Chat does after a
// successful HTTP call.
func TestGeminiResponseParsesFunctionCallParts(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "I'll check that file."},
					{"functionCall": {"name": "read_file", "args": {"path": "foo.txt"}}}
				]
			}
		}]
	}`
	var gr geminiResponse
	if err := json.Unmarshal([]byte(raw), &gr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(gr.Candidates) != 1 || len(gr.Candidates[0].Content.Parts) != 2 {
		t.Fatalf("unexpected shape: %+v", gr)
	}
	textPart := gr.Candidates[0].Content.Parts[0]
	if textPart.Text != "I'll check that file." || textPart.FunctionCall != nil {
		t.Fatalf("expected a plain text part first, got %+v", textPart)
	}
	callPart := gr.Candidates[0].Content.Parts[1]
	if callPart.FunctionCall == nil || callPart.FunctionCall.Name != "read_file" {
		t.Fatalf("expected a functionCall part second, got %+v", callPart)
	}
	if callPart.FunctionCall.Args["path"] != "foo.txt" {
		t.Fatalf("got args %+v", callPart.FunctionCall.Args)
	}
}
