package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fastllm/internal/config"
	"fastllm/internal/imagegen"
)

func configuredImageEndpoint(settings *config.Settings) *config.ModelEndpoint {
	if settings == nil {
		return nil
	}
	if endpoint := settings.FindModel("qwen-image"); endpoint != nil {
		return endpoint
	}
	for i := range settings.Models {
		endpoint := &settings.Models[i]
		if strings.Contains(strings.ToLower(endpoint.ID), "image") || strings.Contains(strings.ToLower(endpoint.Name), "image") {
			return endpoint
		}
	}
	return nil
}

func generateConfiguredImage(ctx context.Context, settings *config.Settings, workingDir, prompt string) (string, int, error) {
	endpoint := configuredImageEndpoint(settings)
	if endpoint == nil {
		return "", 0, fmt.Errorf("no image model is configured; add a model whose id or name contains %q", "image")
	}
	images, err := imagegen.New(endpoint.URL, endpoint.ResolveAPIKey()).Generate(ctx, imagegen.Request{Prompt: prompt})
	if err != nil {
		return "", 0, err
	}
	path, err := imagegen.Save(filepath.Join(workingDir, "generated-images"), images[0])
	if err != nil {
		return "", 0, err
	}
	return path, len(images[0].Bytes), nil
}
