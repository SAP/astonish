package xai_oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

// xaiModelResponse represents a single model from the xAI API.
type xaiModelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// xaiModelsResponse represents the xAI models list API response.
type xaiModelsResponse struct {
	Object string             `json:"object"`
	Data   []xaiModelResponse `json:"data"`
}

// ListModels fetches available models from the xAI API using an OAuth access token.
func ListModels(ctx context.Context, accessToken string) ([]string, error) {
	return listModelsFromURL(ctx, accessToken, apiBaseURL+"/models")
}

// ListModelsWithOAuth lists models with the same renewal and recovery as inference.
func ListModelsWithOAuth(ctx context.Context, clientID, accessToken, refreshToken string, expiresAt time.Time, onRefresh func(string, string, time.Time)) ([]string, error) {
	transport := NewOAuthTransport(clientID, accessToken, refreshToken, expiresAt, onRefresh)
	return listModelsWithClient(ctx, accessToken, apiBaseURL+"/models", &http.Client{Transport: transport, Timeout: 30 * time.Second})
}

// listModelsFromURL preserves access-only setup behavior.
func listModelsFromURL(ctx context.Context, accessToken, endpoint string) ([]string, error) {
	return listModelsWithClient(ctx, accessToken, endpoint, httpool.Client(30*time.Second))
}

func listModelsWithClient(ctx context.Context, accessToken, endpoint string, client *http.Client) ([]string, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

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
		// 401/403 mean the access token is missing/expired/invalid. Signal that
		// the user must re-authenticate so callers can offer the re-auth flow
		// instead of surfacing a raw "bad-credentials" error.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w: models request returned %s: %s", ErrReauthRequired, resp.Status, string(body))
		}
		return nil, fmt.Errorf("models request returned %s: %s", resp.Status, string(body))
	}

	var result xaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}

	var ids []string
	for _, m := range result.Data {
		ids = append(ids, m.ID)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no models found from xAI API")
	}

	sort.Strings(ids)
	return ids, nil
}
