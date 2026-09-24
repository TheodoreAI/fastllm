package llm

import (
	"testing"
)

func TestToAnthropicRequestWithAttachments(t *testing.T) {
	messages := []Message{
		{
			Role:    "user",
			Content: "Please review these items",
			Attachments: []Attachment{
				{
					Type:     "image",
					MimeType: "image/png",
					Name:     "mockup.png",
					DataURI:  "data:image/png;base64,aW1hZ2ViNjQ=",
				},
				{
					Type:     "pdf",
					MimeType: "application/pdf",
					Name:     "spec.pdf",
					DataURI:  "data:application/pdf;base64,cGRmYjY0",
				},
			},
		},
	}

	req := toAnthropicRequest("claude-3-7-sonnet-20250219", messages, nil, false, "")
	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}

	blocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("expected []anthropicContentBlock, got %T", req.Messages[0].Content)
	}

	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (text + image + document), got %d", len(blocks))
	}

	if blocks[0].Type != "text" || blocks[0].Text != "Please review these items" {
		t.Errorf("expected text block, got %+v", blocks[0])
	}

	if blocks[1].Type != "image" || blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" {
		t.Errorf("expected image block, got %+v", blocks[1])
	}

	if blocks[2].Type != "document" || blocks[2].Source == nil || blocks[2].Source.MediaType != "application/pdf" {
		t.Errorf("expected document block, got %+v", blocks[2])
	}
}
