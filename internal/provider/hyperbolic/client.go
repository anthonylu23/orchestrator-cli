package hyperbolic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

const defaultBaseURL = "https://api.hyperbolic.xyz"

type Client interface {
	ValidateAuth(ctx context.Context) error
	ListVirtualMachineOptions(ctx context.Context) ([]VirtualMachineOption, error)
	RentVirtualMachine(ctx context.Context, req VirtualMachineRentalRequest) (VirtualMachineRental, error)
	ListVirtualMachineInstances(ctx context.Context) ([]Instance, error)
	GetVirtualMachineInstance(ctx context.Context, id string) (Instance, error)
	TerminateVirtualMachine(ctx context.Context, id string) error
}

type realClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newRealClient(config Config) (Client, error) {
	secret, err := config.Credentials.Resolve(credentials.Query{
		Provider: "hyperbolic",
		Name:     "api_key",
		Env:      []string{"HYPERBOLIC_API_KEY"},
	})
	if err != nil {
		return nil, &apiError{StatusCode: http.StatusUnauthorized, Code: "missing-api-key", Message: "hyperbolic/api_key credential or HYPERBOLIC_API_KEY is required; run `switchboard-cli credentials set hyperbolic api-key`"}
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
	var out struct {
		Credits int `json:"credits"`
	}
	return c.do(ctx, http.MethodGet, "/billing/get_current_balance", nil, &out)
}

func (c *realClient) ListVirtualMachineOptions(ctx context.Context) ([]VirtualMachineOption, error) {
	var out []VirtualMachineOption
	if err := c.do(ctx, http.MethodGet, "/v2/marketplace/virtual-machine-options", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *realClient) RentVirtualMachine(ctx context.Context, req VirtualMachineRentalRequest) (VirtualMachineRental, error) {
	var out VirtualMachineRental
	if err := c.do(ctx, http.MethodPost, "/v2/marketplace/virtual-machine-rentals", req, &out); err != nil {
		return VirtualMachineRental{}, err
	}
	return out, nil
}

func (c *realClient) ListVirtualMachineInstances(ctx context.Context) ([]Instance, error) {
	var out []Instance
	if err := c.do(ctx, http.MethodGet, "/v2/marketplace/virtual-machine-rentals", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *realClient) GetVirtualMachineInstance(ctx context.Context, id string) (Instance, error) {
	instances, err := c.ListVirtualMachineInstances(ctx)
	if err != nil {
		return Instance{}, err
	}
	for _, instance := range instances {
		if instance.RefID() == id || instance.ExternalID == id {
			return instance, nil
		}
	}
	return Instance{}, &apiError{StatusCode: http.StatusNotFound, Code: "not-found", Message: fmt.Sprintf("hyperbolic virtual machine rental %q was not found", id)}
}

func (c *realClient) TerminateVirtualMachine(ctx context.Context, id string) error {
	rentalID, err := strconv.Atoi(id)
	if err != nil {
		return &apiError{StatusCode: http.StatusBadRequest, Code: "invalid-rental-id", Message: fmt.Sprintf("hyperbolic on-demand VM rental id %q is not numeric", id)}
	}
	req := struct {
		RentalID int `json:"rentalId"`
	}{RentalID: rentalID}
	return c.do(ctx, http.MethodPost, "/v2/marketplace/virtual-machine-rentals/terminate", req, nil)
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
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
		return parseAPIError(resp.StatusCode, content)
	}
	if out == nil {
		return nil
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return nil
	}
	if err := json.Unmarshal(content, out); err != nil {
		return fmt.Errorf("decode hyperbolic api response: %w", err)
	}
	return nil
}

func parseAPIError(statusCode int, content []byte) error {
	var parsed struct {
		Error   interface{} `json:"error"`
		Code    string      `json:"code"`
		Msg     string      `json:"msg"`
		Message string      `json:"message"`
	}
	if err := json.Unmarshal(content, &parsed); err == nil {
		code := parsed.Code
		message := parsed.Message
		switch value := parsed.Error.(type) {
		case map[string]interface{}:
			if code == "" {
				if raw, ok := value["code"].(string); ok {
					code = raw
				}
			}
			if message == "" {
				if raw, ok := value["message"].(string); ok {
					message = raw
				}
			}
		case string:
			if message == "" {
				message = value
			}
		}
		if message == "" {
			message = parsed.Msg
		}
		if code != "" || message != "" {
			return &apiError{StatusCode: statusCode, Code: code, Message: message}
		}
	}
	return &apiError{StatusCode: statusCode, Code: fmt.Sprintf("http/%d", statusCode), Message: string(content)}
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *apiError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "hyperbolic api error"
}
