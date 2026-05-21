package redact

import (
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
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

func TestSanitizeURIRedactsSignedQueryAndUserinfo(t *testing.T) {
	got := SanitizeURI("https://user:pass@example.com/checkpoint.pt?X-Amz-Credential=cred&X-Amz-Signature=sig&part=1#token")
	for _, leaked := range []string{"user", "pass", "cred", "sig", "token"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitized uri leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "REDACTED") || strings.Contains(got, "part=1") {
		t.Fatalf("sanitized uri = %s", got)
	}
}

func TestStringRedactsSignedURIReferencesInPlainText(t *testing.T) {
	redactor := FromEnvironment()
	got := redactor.String("resume from https://example.com/ckpt.pt?X-Goog-Signature=secret-signature")
	if strings.Contains(got, "secret-signature") {
		t.Fatalf("string leaked signed URI: %s", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("string missing redaction marker: %s", got)
	}
}

func TestEventRedactsCheckpointURIInTypedAndFieldCopies(t *testing.T) {
	redactor := FromEnvironment()
	uri := "s3://bucket/ckpt.pt?token=secret-token-value"
	ev := redactor.Event(app.Event{
		Type:          app.EventTypeCheckpoint,
		CheckpointURI: uri,
		Fields:        map[string]interface{}{"checkpoint_uri": uri},
	})
	if strings.Contains(ev.CheckpointURI, "secret-token-value") {
		t.Fatalf("checkpoint uri = %q", ev.CheckpointURI)
	}
	if strings.Contains(ev.Fields["checkpoint_uri"].(string), "secret-token-value") {
		t.Fatalf("fields = %#v", ev.Fields)
	}
}
