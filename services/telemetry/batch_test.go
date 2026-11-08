package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

// reading returns a valid telemetria.bruta record.
func reading(machineID string, bootID uint32, seq uint64) *kgo.Record {
	value, err := json.Marshal(contract.Telemetry{
		SchemaVersion: contract.SchemaVersion,
		MachineID:     machineID,
		BootID:        bootID,
		Seq:           seq,
		TS:            time.Date(2026, 11, 7, 14, 0, int(seq), 0, time.UTC),
		State:         contract.StateRunning,
	})
	if err != nil {
		panic(err)
	}
	return &kgo.Record{Topic: contract.TopicTelemetry, Key: []byte(machineID), Value: value}
}

func TestBatchAdd(t *testing.T) {
	invalid := &kgo.Record{Topic: contract.TopicTelemetry, Key: []byte("maq-01"), Value: []byte(`{"machine_id":"maq-01"}`)}
	tests := []struct {
		name           string
		records        []*kgo.Record
		wantRows       []string // machine_id/boot_id/seq of each row, in order
		wantDuplicates int
		wantDead       int
	}{
		{
			name:     "readings in order",
			records:  []*kgo.Record{reading("maq-01", 7, 0), reading("maq-01", 7, 1), reading("maq-01", 7, 2)},
			wantRows: []string{"maq-01/7/0", "maq-01/7/1", "maq-01/7/2"},
		},
		{
			name:           "redelivered reading",
			records:        []*kgo.Record{reading("maq-01", 7, 0), reading("maq-01", 7, 1), reading("maq-01", 7, 1)},
			wantRows:       []string{"maq-01/7/0", "maq-01/7/1"},
			wantDuplicates: 1,
		},
		{
			name:           "older reading after a newer one",
			records:        []*kgo.Record{reading("maq-01", 7, 5), reading("maq-01", 7, 3)},
			wantRows:       []string{"maq-01/7/5"},
			wantDuplicates: 1,
		},
		{
			name:     "restart starts seq again under a new boot_id",
			records:  []*kgo.Record{reading("maq-01", 7, 900), reading("maq-01", 8, 0), reading("maq-01", 8, 1)},
			wantRows: []string{"maq-01/7/900", "maq-01/8/0", "maq-01/8/1"},
		},
		{
			name:     "machines are tracked apart",
			records:  []*kgo.Record{reading("maq-01", 7, 4), reading("maq-02", 7, 4)},
			wantRows: []string{"maq-01/7/4", "maq-02/7/4"},
		},
		{
			name:     "message that breaks the contract",
			records:  []*kgo.Record{reading("maq-01", 7, 0), invalid, reading("maq-01", 7, 1)},
			wantRows: []string{"maq-01/7/0", "maq-01/7/1"},
			wantDead: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b batch
			seen := dedup{}
			for _, r := range tt.records {
				b.add(r, seen)
			}
			var rows []string
			for _, r := range b.rows {
				rows = append(rows, fmt.Sprintf("%s/%d/%d", r.MachineID, r.BootID, r.Seq))
			}
			if fmt.Sprint(rows) != fmt.Sprint(tt.wantRows) || b.duplicates != tt.wantDuplicates || len(b.dead) != tt.wantDead {
				t.Errorf("got rows %v, %d duplicates, %d dead; want rows %v, %d duplicates, %d dead",
					rows, b.duplicates, len(b.dead), tt.wantRows, tt.wantDuplicates, tt.wantDead)
			}
			if b.records != len(tt.records) {
				t.Errorf("counted %d records, want %d", b.records, len(tt.records))
			}
		})
	}
}

func TestDedupSurvivesBatches(t *testing.T) {
	seen := dedup{}
	var first, second batch
	first.add(reading("maq-01", 7, 10), seen)
	second.add(reading("maq-01", 7, 10), seen)
	second.add(reading("maq-01", 7, 11), seen)
	if len(second.rows) != 1 || second.duplicates != 1 {
		t.Errorf("second batch: %d rows and %d duplicates, want 1 and 1", len(second.rows), second.duplicates)
	}
}

func TestDeadLetterRecord(t *testing.T) {
	var b batch
	b.add(&kgo.Record{Topic: contract.TopicTelemetry, Key: []byte("maq-01"), Value: []byte(`{"machine_id":"maq-01"}`)}, dedup{})
	r := b.dead[0]
	want := map[string]string{
		dlq.HeaderError:   "schema_version: missing",
		dlq.HeaderSource:  "kafka:telemetria.bruta",
		dlq.HeaderService: "telemetry",
	}
	for key, value := range want {
		if got := dlq.Header(r, key); got != value {
			t.Errorf("header %s = %q, want %q", key, got, value)
		}
	}
	if r.Topic != contract.TopicDLQ || string(r.Key) != "maq-01" || string(r.Value) != `{"machine_id":"maq-01"}` {
		t.Errorf("got topic %q key %q value %s, want the DLQ with the original key and value", r.Topic, r.Key, r.Value)
	}
}
