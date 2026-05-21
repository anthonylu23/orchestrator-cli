package redact

import (
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

const Replacement = "[REDACTED]"

var uriReferencePattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

type Redactor struct {
	values []string
}

func FromEnvironment(envMaps ...map[string]string) Redactor {
	values := map[string]bool{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !IsSecretKey(key) || len(value) < 6 {
			continue
		}
		values[value] = true
	}
	for _, env := range envMaps {
		for key, value := range env {
			if !IsSecretKey(key) || len(value) < 6 {
				continue
			}
			values[value] = true
		}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i]) > len(out[j])
	})
	return Redactor{values: out}
}

func IsSecretKey(key string) bool {
	normalized := strings.ToLower(key)
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	normalized = replacer.Replace(normalized)
	for _, marker := range []string{
		"password",
		"passwd",
		"secret",
		"token",
		"apikey",
		"accesskey",
		"privatekey",
		"credential",
		"authorization",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func IsURIKey(key string) bool {
	normalized := strings.ToLower(key)
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	normalized = replacer.Replace(normalized)
	for _, marker := range []string{"uri", "url", "source"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func SanitizeURI(value string) string {
	if value == "" {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	changed := false
	if parsed.User != nil {
		parsed.User = url.User(Replacement)
		changed = true
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		redactAll := queryHasSignedURLMarkers(query)
		for key, values := range query {
			if redactAll || isSecretQueryKey(key) {
				for i := range values {
					values[i] = Replacement
				}
				query[key] = values
				changed = true
			}
		}
		parsed.RawQuery = query.Encode()
	}
	if parsed.Fragment != "" {
		parsed.Fragment = Replacement
		changed = true
	}
	if !changed {
		return value
	}
	return parsed.String()
}

func SanitizeURIReferences(value string) string {
	if !strings.Contains(value, "://") {
		return value
	}
	return uriReferencePattern.ReplaceAllStringFunc(value, func(match string) string {
		return SanitizeURI(match)
	})
}

func SanitizeEventURIs(ev app.Event) app.Event {
	ev.CheckpointURI = SanitizeURI(ev.CheckpointURI)
	ev.Message = SanitizeURIReferences(ev.Message)
	if ev.Fields != nil {
		if redacted, ok := sanitizeURIValue("", ev.Fields).(map[string]interface{}); ok {
			ev.Fields = redacted
		}
	}
	return ev
}

func SanitizeAttemptURIs(attempt app.Attempt) app.Attempt {
	attempt.ResumeFromURI = SanitizeURI(attempt.ResumeFromURI)
	attempt.ExitReason = SanitizeURIReferences(attempt.ExitReason)
	attempt.ProviderRef = SanitizeURIReferences(attempt.ProviderRef)
	return attempt
}

func (r Redactor) String(value string) string {
	out := value
	for _, secret := range r.values {
		out = strings.ReplaceAll(out, secret, Replacement)
	}
	return SanitizeURIReferences(out)
}

func (r Redactor) URI(value string) string {
	return r.String(SanitizeURI(value))
}

func (r Redactor) Line(value string) string {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return r.String(value)
	}
	redacted, ok := r.value("", raw).(map[string]interface{})
	if !ok {
		return r.String(value)
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return r.String(value)
	}
	return string(encoded)
}

func (r Redactor) Event(ev app.Event) app.Event {
	ev.RunID = r.String(ev.RunID)
	ev.AttemptID = r.String(ev.AttemptID)
	ev.Split = r.String(ev.Split)
	ev.State = r.String(ev.State)
	ev.CheckpointURI = r.URI(ev.CheckpointURI)
	ev.Message = r.String(ev.Message)
	if ev.Fields != nil {
		if redacted, ok := r.value("", ev.Fields).(map[string]interface{}); ok {
			ev.Fields = redacted
		}
	}
	return ev
}

func (r Redactor) Summary(summary app.Summary) app.Summary {
	summary.RunID = r.String(summary.RunID)
	summary.ExitReason = r.String(summary.ExitReason)
	for i := range summary.ProviderAttempts {
		summary.ProviderAttempts[i] = r.Attempt(summary.ProviderAttempts[i])
	}
	return summary
}

func (r Redactor) Run(run app.Run) app.Run {
	run.ID = r.String(run.ID)
	run.JobName = r.String(run.JobName)
	run.Script = r.String(run.Script)
	run.Image = r.String(run.Image)
	run.Provider = r.String(run.Provider)
	run.Error = r.String(run.Error)
	return run
}

func (r Redactor) Attempt(attempt app.Attempt) app.Attempt {
	attempt.ID = r.String(attempt.ID)
	attempt.RunID = r.String(attempt.RunID)
	attempt.Provider = r.String(attempt.Provider)
	attempt.ExitReason = r.String(attempt.ExitReason)
	attempt.ProviderRef = r.String(attempt.ProviderRef)
	attempt.ResumeFromURI = r.URI(attempt.ResumeFromURI)
	attempt.EstimateCurrency = r.String(attempt.EstimateCurrency)
	return attempt
}

func (r Redactor) value(key string, value interface{}) interface{} {
	if IsSecretKey(key) {
		return Replacement
	}
	switch typed := value.(type) {
	case string:
		if IsURIKey(key) {
			return r.URI(typed)
		}
		return r.String(typed)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			out[k] = r.value(k, v)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			if IsSecretKey(k) {
				out[k] = Replacement
			} else if IsURIKey(k) {
				out[k] = r.URI(v)
			} else {
				out[k] = r.String(v)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, v := range typed {
			out[i] = r.value("", v)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, v := range typed {
			out[i] = r.String(v)
		}
		return out
	default:
		return value
	}
}

func sanitizeURIValue(key string, value interface{}) interface{} {
	if IsSecretKey(key) {
		return Replacement
	}
	switch typed := value.(type) {
	case string:
		if IsURIKey(key) {
			return SanitizeURI(typed)
		}
		return SanitizeURIReferences(typed)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			out[k] = sanitizeURIValue(k, v)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, v := range typed {
			out[i] = sanitizeURIValue("", v)
		}
		return out
	default:
		return value
	}
}

func queryHasSignedURLMarkers(query url.Values) bool {
	for key := range query {
		normalized := normalizeQueryKey(key)
		for _, marker := range []string{
			"xamzsignature",
			"xamzcredential",
			"xamzsecuritytoken",
			"xgoogsignature",
			"xgoogcredential",
			"xgoogsecuritytoken",
			"awsaccesskeyid",
			"signature",
			"sig",
		} {
			if normalized == marker {
				return true
			}
		}
	}
	return false
}

func isSecretQueryKey(key string) bool {
	if IsSecretKey(key) {
		return true
	}
	normalized := normalizeQueryKey(key)
	for _, marker := range []string{"signature", "sig", "credential", "securitytoken", "accesskeyid", "secret"} {
		if normalized == marker || strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func normalizeQueryKey(key string) string {
	key = strings.ToLower(key)
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	return replacer.Replace(key)
}
