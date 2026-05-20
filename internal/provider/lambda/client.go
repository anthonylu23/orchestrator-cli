package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

const defaultBaseURL = "https://cloud.lambda.ai"

type Client interface {
	ValidateAuth(ctx context.Context) error
	ListInstanceTypes(ctx context.Context) (map[string]InstanceTypesItem, error)
	ListInstances(ctx context.Context) ([]Instance, error)
	GetInstance(ctx context.Context, id string) (Instance, error)
	LaunchInstance(ctx context.Context, req LaunchInstanceRequest) ([]string, error)
	TerminateInstances(ctx context.Context, ids []string) error
}

type realClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newRealClient(config Config) (Client, error) {
	secret, err := config.Credentials.Resolve(credentials.Query{
		Provider: "lambda",
		Name:     "api_key",
	})
	if err != nil {
		return nil, &apiError{StatusCode: http.StatusUnauthorized, Code: "global/missing-api-key", Message: "stored lambda/api_key credential is required; run `switchboard-cli credentials set lambda api-key`"}
	}
	timeout := time.Duration(config.APITimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &realClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     secret.Value,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

func (c *realClient) ValidateAuth(ctx context.Context) error {
	_, err := c.ListInstances(ctx)
	return err
}

func (c *realClient) ListInstanceTypes(ctx context.Context) (map[string]InstanceTypesItem, error) {
	var out struct {
		Data map[string]InstanceTypesItem `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instance-types", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *realClient) ListInstances(ctx context.Context) ([]Instance, error) {
	var out struct {
		Data []Instance `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instances", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (c *realClient) GetInstance(ctx context.Context, id string) (Instance, error) {
	var out struct {
		Data Instance `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+url.PathEscape(id), nil, &out); err != nil {
		return Instance{}, err
	}
	return out.Data, nil
}

func (c *realClient) LaunchInstance(ctx context.Context, req LaunchInstanceRequest) ([]string, error) {
	var out struct {
		Data struct {
			InstanceIDs []string `json:"instance_ids"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/instance-operations/launch", req, &out); err != nil {
		return nil, err
	}
	return out.Data.InstanceIDs, nil
}

func (c *realClient) TerminateInstances(ctx context.Context, ids []string) error {
	req := struct {
		InstanceIDs []string `json:"instance_ids"`
	}{InstanceIDs: ids}
	var out struct {
		Data interface{} `json:"data"`
	}
	return c.do(ctx, http.MethodPost, "/api/v1/instance-operations/terminate", req, &out)
}

func (c *realClient) do(ctx context.Context, method string, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed lambdaErrorResponse
		if err := json.Unmarshal(content, &parsed); err == nil && parsed.Error.Code != "" {
			return &apiError{StatusCode: resp.StatusCode, Code: parsed.Error.Code, Message: parsed.Error.Message, Suggestion: parsed.Error.Suggestion}
		}
		return &apiError{StatusCode: resp.StatusCode, Code: fmt.Sprintf("http/%d", resp.StatusCode), Message: string(content)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(content, out); err != nil {
		return fmt.Errorf("decode lambda api response: %w", err)
	}
	return nil
}
