package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

var examplesDir = filepath.Join("..", "..", "contracts", "examples", "telemetry")

type invalidCase struct {
	Case    string          `json:"case"`
	Field   string          `json:"field"`
	Message json.RawMessage `json:"message"`
}

func TestRouteForwardsValidMessages(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(examplesDir, "valid", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no valid examples found: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			payload, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var msg struct {
				MachineID string `json:"machine_id"`
			}
			if err := json.Unmarshal(payload, &msg); err != nil {
				t.Fatal(err)
			}
			r := route(contract.TelemetryTopic(msg.MachineID), payload)
			if r.Topic != contract.TopicTelemetry || string(r.Key) != msg.MachineID || len(r.Headers) != 0 {
				t.Errorf("got topic %q key %q headers %v, want %q key %q and no headers", r.Topic, r.Key, r.Headers, contract.TopicTelemetry, msg.MachineID)
			}
			if !bytes.Equal(r.Value, payload) {
				t.Errorf("payload changed on the way to Kafka:\n%s", r.Value)
			}
		})
	}
}

func TestRouteSendsInvalidMessagesToDLQ(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(examplesDir, "invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []invalidCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	// Malformed JSON has no field to blame. Its error starts with this text instead.
	cases = append(cases, invalidCase{
		Case:    "malformed_json",
		Field:   "message is not a JSON object",
		Message: json.RawMessage(`{"machine_id": "maq-01",`),
	})

	const topic = "fabrica/maq-01/telemetria"
	for _, c := range cases {
		t.Run(c.Case, func(t *testing.T) {
			r := route(topic, c.Message)
			if r.Topic != contract.TopicDLQ || string(r.Key) != "maq-01" || !bytes.Equal(r.Value, c.Message) {
				t.Errorf("got topic %q key %q value %s, want %q key %q and the original message", r.Topic, r.Key, r.Value, contract.TopicDLQ, "maq-01")
			}
			if reason := dlq.Header(r, dlq.HeaderError); !strings.HasPrefix(reason, c.Field+": ") {
				t.Errorf("error header %q, want it to start with %q", reason, c.Field+": ")
			}
			if got := dlq.Header(r, dlq.HeaderSource); got != "mqtt:"+topic {
				t.Errorf("source header %q, want %q", got, "mqtt:"+topic)
			}
			if got := dlq.Header(r, dlq.HeaderService); got != "bridge" {
				t.Errorf("service header %q, want %q", got, "bridge")
			}
		})
	}
}
