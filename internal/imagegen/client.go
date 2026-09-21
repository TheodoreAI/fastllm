// Package imagegen calls OpenAI-compatible image generation endpoints and
// persists their base64 responses as ordinary image files.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxResponseBytes = 64 << 20

// Client is an OpenAI-compatible image generation client.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Request describes one image generation request.
type Request struct {
	Prompt string
	Model  string
	Size   string
	Steps  int
	Count  int
}

// Image is one decoded image returned by the generation endpoint.
type Image struct {
	Bytes     []byte
	MediaType string
	Extension string
}

// New constructs an image client. baseURL should include the API version,
// for example http://127.0.0.1:8011/v1.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     strings.TrimSpace(apiKey),
		HTTPClient: http.DefaultClient,
	}
}

// Generate requests images and decodes data[].b64_json into binary data.
func (c *Client) Generate(ctx context.Context, req Request) ([]Image, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("image endpoint URL is empty")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("image prompt is empty")
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Size == "" {
		req.Size = "1024x1024"
	}
	if req.Steps <= 0 {
		req.Steps = 35
	}

	payload := struct {
		Prompt         string `json:"prompt"`
		Model          string `json:"model,omitempty"`
		Count          int    `json:"n"`
		Size           string `json:"size"`
		ResponseFormat string `json:"response_format"`
		Steps          int    `json:"steps"`
	}{
		Prompt:         req.Prompt,
		Model:          req.Model,
		Count:          req.Count,
		Size:           req.Size,
		ResponseFormat: "b64_json",
		Steps:          req.Steps,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode image request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("generate image: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, fmt.Errorf("image response exceeds %d MiB limit", maxResponseBytes>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 500 {
			message = message[:500] + "..."
		}
		return nil, fmt.Errorf("image endpoint returned %s: %s", resp.Status, message)
	}

	var decoded struct {
		Data []struct {
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode image response JSON: %w", err)
	}
	if len(decoded.Data) == 0 {
		return nil, fmt.Errorf("image response contains no data")
	}

	images := make([]Image, 0, len(decoded.Data))
	for i, item := range decoded.Data {
		encoded := stripDataURIPrefix(strings.TrimSpace(item.Base64))
		if encoded == "" {
			return nil, fmt.Errorf("image %d contains no b64_json data", i+1)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode image %d base64: %w", i+1, err)
		}
		mediaType, extension, ok := imageFormat(raw)
		if !ok {
			return nil, fmt.Errorf("image %d has an unsupported or invalid file signature", i+1)
		}
		images = append(images, Image{Bytes: raw, MediaType: mediaType, Extension: extension})
	}
	return images, nil
}

func stripDataURIPrefix(encoded string) string {
	if !strings.HasPrefix(encoded, "data:") {
		return encoded
	}
	comma := strings.IndexByte(encoded, ',')
	if comma < 0 || !strings.Contains(encoded[:comma], ";base64") {
		return ""
	}
	return encoded[comma+1:]
}

func imageFormat(data []byte) (mediaType, extension string, ok bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", ".png", true
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", ".jpg", true
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif", ".gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", ".webp", true
	default:
		return "", "", false
	}
}

// Save writes an image into dir using a collision-resistant timestamped name.
func Save(dir string, image Image) (string, error) {
	if len(image.Bytes) == 0 {
		return "", fmt.Errorf("cannot save an empty image")
	}
	if image.Extension == "" {
		return "", fmt.Errorf("cannot save an image without a file extension")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create image directory: %w", err)
	}

	base := "image-" + time.Now().Format("20060102-150405.000000000")
	for attempt := 0; attempt < 100; attempt++ {
		name := base
		if attempt > 0 {
			name += fmt.Sprintf("-%d", attempt)
		}
		path := filepath.Join(dir, name+image.Extension)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create image file: %w", err)
		}
		if _, err := file.Write(image.Bytes); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("write image file: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("close image file: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("could not allocate a unique image filename")
}
