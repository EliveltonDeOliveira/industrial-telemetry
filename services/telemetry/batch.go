package main

import (
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/dlq"
)

// bootKey identifies one power cycle of a machine. seq only grows within it.
type bootKey struct {
	machineID string
	bootID    uint32
}

// dedup holds the last seq seen for each (machine_id, boot_id) (ADR 0003).
// It lives in memory and is lost on restart; duplicates that pass then are
// removed by the ReplacingMergeTree of telemetry_raw.
type dedup map[bootKey]uint64

// batch collects consumed records until they are stored and committed.
type batch struct {
	started    time.Time // when the first record arrived
	records    int       // every record consumed, including dropped ones
	rows       []contract.Telemetry
	dead       []*kgo.Record
	duplicates int
}

// add classifies one record: a message that breaks the contract goes to the
// DLQ, a seq already seen is dropped, and anything else becomes a row.
func (b *batch) add(r *kgo.Record, seen dedup) {
	if b.records == 0 {
		b.started = time.Now()
	}
	b.records++

	t, err := contract.ParseTelemetry(r.Value)
	if err != nil {
		b.dead = append(b.dead, dlq.Record(serviceName, "kafka:"+r.Topic, r.Key, r.Value, err))
		return
	}
	k := bootKey{t.MachineID, t.BootID}
	if last, ok := seen[k]; ok && t.Seq <= last {
		b.duplicates++
		return
	}
	seen[k] = t.Seq
	b.rows = append(b.rows, t)
}
