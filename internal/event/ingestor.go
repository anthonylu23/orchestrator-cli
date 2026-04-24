package event

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

type ParsedLine struct {
	Event      app.Event
	Structured bool
}

func ParseLine(line string, runID string, attemptID string, now time.Time) ParsedLine {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ParsedLine{Event: app.Event{
			Type:      app.EventTypeLog,
			RunID:     runID,
			AttemptID: attemptID,
			Timestamp: now,
			Message:   line,
		}}
	}
	typeValue, _ := raw["type"].(string)
	switch app.EventType(typeValue) {
	case app.EventTypeMetric, app.EventTypeCheckpoint, app.EventTypeStatus:
	default:
		return ParsedLine{Event: app.Event{
			Type:      app.EventTypeLog,
			RunID:     runID,
			AttemptID: attemptID,
			Timestamp: now,
			Message:   line,
		}}
	}

	ev := app.Event{
		Type:      app.EventType(typeValue),
		RunID:     stringValue(raw, "run_id", runID),
		AttemptID: stringValue(raw, "attempt_id", attemptID),
		Timestamp: parseTime(raw, now),
		Split:     stringValue(raw, "split", ""),
		State:     stringValue(raw, "state", ""),
		Fields:    raw,
	}
	ev.Step = intPointer(raw, "step")
	ev.Epoch = intPointer(raw, "epoch")
	ev.CheckpointURI = stringValue(raw, "checkpoint_uri", "")
	ev.Metrics = metrics(raw)
	return ParsedLine{Event: ev, Structured: true}
}

func WriteJSONL(w io.Writer, ev app.Event) error {
	encoded, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", encoded)
	return err
}

func ReadJSONL(path string) ([]app.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []app.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var ev app.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func stringValue(raw map[string]interface{}, key string, fallback string) string {
	value, ok := raw[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func parseTime(raw map[string]interface{}, fallback time.Time) time.Time {
	value, ok := raw["ts"].(string)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fallback
	}
	return parsed
}

func intPointer(raw map[string]interface{}, key string) *int64 {
	value, ok := raw[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case float64:
		i := int64(typed)
		return &i
	case int64:
		i := typed
		return &i
	default:
		return nil
	}
}

func metrics(raw map[string]interface{}) map[string]float64 {
	value, ok := raw["metrics"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]float64, len(value))
	for k, v := range value {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
