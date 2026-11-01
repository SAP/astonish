package copilot_oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// copilotModelResponse represents a single model from the Copilot API.
type copilotModelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// copilotModelsResponse represents the Copilot models list API response.
type copilotModelsResponse struct {
	Object string                 `json:"object"`
	Data   []copilotModelResponse `json:"data"`
}

// ListModels fetches available models from the Copilot API. It first exchanges
// the GitHub token for a Copilot session token, then queries the models endpoint.
func ListModels(ctx context.Context, githubToken string) ([]string, error) {
	return listModelsFromURL(ctx, githubToken, apiBaseURL+"/models", "")
}

// listModelsFromURL is the internal implementation that accepts custom URLs for testing.
func listModelsFromURL(ctx context.Context, githubToken, modelsEndpoint, tokenEndpoint string) ([]string, error) {
	if githubToken == "" {
		return nil, fmt.Errorf("github token is required")
	}

	transport := NewCopilotTransport(githubToken)
	transport.copilotTokenURL = tokenEndpoint

	req, err := http.NewRequestWithContext(ctx, "GET", modelsEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}

	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Editor-Version", "vscode/1.107.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w: models request returned HTTP %d", ErrReauthRequired, resp.StatusCode)
		}
		return nil, fmt.Errorf("models request returned %s: %s", resp.Status, string(body))
	}

	var result copilotModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}

	var ids []string
	for _, m := range result.Data {
		ids = append(ids, m.ID)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no models found from Copilot API")
	}

	sort.Strings(ids)
	return ids, nil
}
