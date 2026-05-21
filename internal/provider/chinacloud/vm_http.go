package chinacloud

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func decodeProviderJSON(provider string, resp *http.Response, out interface{}) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("%s response read failed: %v", provider, err), Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerHTTPError(provider, resp.StatusCode, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("%s response parse failed: %v", provider, err), Err: err}
	}
	return nil
}

func providerHTTPError(provider string, status int, body []byte) error {
	kind := app.ProviderErrorUnknown
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = app.ProviderErrorAuth
	case status == http.StatusTooManyRequests:
		kind = app.ProviderErrorQuota
	case status == http.StatusConflict:
		kind = app.ProviderErrorCapacity
	case status >= 400 && status < 500:
		kind = app.ProviderErrorInvalidSpec
	case status >= 500:
		kind = app.ProviderErrorInternal
	}
	message := fmt.Sprintf("%s request returned HTTP %d", provider, status)
	if len(body) > 0 {
		message = fmt.Sprintf("%s: %s", message, string(body))
	}
	return &app.ProviderError{Kind: kind, Message: message}
}
