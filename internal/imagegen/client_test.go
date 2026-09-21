package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tinyPNG = []byte("\x89PNG\r\n\x1a\nnot-a-full-png-but-a-valid-signature")

func TestGenerateDecodesBase64Image(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["response_format"] != "b64_json" || request["prompt"] != "draw a robot" {
			t.Fatalf("request = %#v", request)
		}
		encoded := base64.StdEncoding.EncodeToString(tinyPNG)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": "data:image/png;base64," + encoded}}})
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "secret")
	images, err := client.Generate(context.Background(), Request{Prompt: "draw a robot"})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || string(images[0].Bytes) != string(tinyPNG) {
		t.Fatalf("images = %#v", images)
	}
	if images[0].MediaType != "image/png" || images[0].Extension != ".png" {
		t.Fatalf("format = %q %q", images[0].MediaType, images[0].Extension)
	}
}

func TestGenerateRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing data", body: `{"data":[]}`, want: "contains no data"},
		{name: "invalid base64", body: `{"data":[{"b64_json":"%%%"}]}`, want: "decode image 1 base64"},
		{name: "not an image", body: `{"data":[{"b64_json":"aGVsbG8="}]}`, want: "invalid file signature"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := New(server.URL, "").Generate(context.Background(), Request{Prompt: "test"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestGenerateReportsEndpointError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"bad key"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := New(server.URL, "wrong").Generate(context.Background(), Request{Prompt: "test"})
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("error = %v", err)
	}
}

func TestSaveWritesDecodedBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "generated-images")
	path, err := Save(dir, Image{Bytes: tinyPNG, Extension: ".png", MediaType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(tinyPNG) {
		t.Fatalf("saved bytes differ")
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("path %q is outside %q", path, dir)
	}
}
