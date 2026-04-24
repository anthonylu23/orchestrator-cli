package event

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func TestParseLineMetric(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	parsed := ParseLine(`{"type":"metric","step":12,"metrics":{"loss":0.25},"split":"train"}`, "r_1", "a_1", now)
	if !parsed.Structured {
		t.Fatal("expected structured event")
	}
	if parsed.Event.Type != app.EventTypeMetric {
		t.Fatalf("type = %q", parsed.Event.Type)
	}
	if parsed.Event.Step == nil || *parsed.Event.Step != 12 {
		t.Fatalf("step = %#v", parsed.Event.Step)
	}
	if parsed.Event.Metrics["loss"] != 0.25 {
		t.Fatalf("metrics = %#v", parsed.Event.Metrics)
	}
}

func TestParseLineMalformedJSONIsLog(t *testing.T) {
	parsed := ParseLine(`{"type":"metric"`, "r_1", "a_1", time.Now())
	if parsed.Structured {
		t.Fatal("expected plain log")
	}
	if parsed.Event.Type != app.EventTypeLog {
		t.Fatalf("type = %q", parsed.Event.Type)
	}
}

func TestParseLineUnknownJSONIsLog(t *testing.T) {
	parsed := ParseLine(`{"hello":"world"}`, "r_1", "a_1", time.Now())
	if parsed.Structured {
		t.Fatal("expected plain log")
	}
	if !strings.Contains(parsed.Event.Message, "hello") {
		t.Fatalf("message = %q", parsed.Event.Message)
	}
}

func TestWriteJSONL(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSONL(&buf, app.Event{Type: app.EventTypeStatus, State: "running"}); err != nil {
		t.Fatalf("WriteJSONL returned error: %v", err)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("expected newline, got %q", buf.String())
	}
}
