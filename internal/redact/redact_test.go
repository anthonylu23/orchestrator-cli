package redact

import (
	"strings"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func TestStringRedactsKnownSecretValues(t *testing.T) {
	redactor := FromEnvironment(map[string]string{"API_TOKEN": "secret-value-123"})
	got := redactor.String("token=secret-value-123")
	if strings.Contains(got, "secret-value-123") || !strings.Contains(got, Replacement) {
		t.Fatalf("redacted string = %q", got)
	}
}

func TestEventRedactsSecretKeysAndNestedValues(t *testing.T) {
	redactor := FromEnvironment(map[string]string{"SERVICE_PASSWORD": "nested-secret-123"})
	ev := redactor.Event(app.Event{
		Type:    app.EventTypeMetric,
		Message: "using nested-secret-123",
		Fields: map[string]interface{}{
			"api_key": "raw-key-value",
			"nested": map[string]interface{}{
				"message": "nested-secret-123",
			},
		},
	})
	if ev.Message != "using "+Replacement {
		t.Fatalf("message = %q", ev.Message)
	}
	if ev.Fields["api_key"] != Replacement {
		t.Fatalf("api_key = %#v", ev.Fields["api_key"])
	}
	nested := ev.Fields["nested"].(map[string]interface{})
	if nested["message"] != Replacement {
		t.Fatalf("nested message = %#v", nested["message"])
	}
}

func TestLineRedactsStructuredSecretKeys(t *testing.T) {
	redactor := FromEnvironment()
	got := redactor.Line(`{"type":"status","api_key":"raw-event-secret"}`)
	if strings.Contains(got, "raw-event-secret") || !strings.Contains(got, Replacement) {
		t.Fatalf("line = %q", got)
	}
}
