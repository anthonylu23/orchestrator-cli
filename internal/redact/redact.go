package redact

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

const Replacement = "[REDACTED]"

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

func (r Redactor) String(value string) string {
	out := value
	for _, secret := range r.values {
		out = strings.ReplaceAll(out, secret, Replacement)
	}
	return out
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
	ev.CheckpointURI = r.String(ev.CheckpointURI)
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
	attempt.ResumeFromURI = r.String(attempt.ResumeFromURI)
	attempt.EstimateCurrency = r.String(attempt.EstimateCurrency)
	return attempt
}

func (r Redactor) value(key string, value interface{}) interface{} {
	if IsSecretKey(key) {
		return Replacement
	}
	switch typed := value.(type) {
	case string:
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
