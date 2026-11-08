package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// TestInsertRows needs the ClickHouse of the compose stack, with migrations applied:
//
//	CLICKHOUSE_TEST_DSN=clickhouse://industrial:industrial@localhost:9000/industrial go test ./services/telemetry
func TestInsertRows(t *testing.T) {
	dsn := os.Getenv("CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLICKHOUSE_TEST_DSN not set")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Cleanups run in reverse order, so the rows are deleted before the connection closes.
	t.Cleanup(func() { conn.Close() })
	machineID := fmt.Sprintf("test-%08x", rand.Uint32())
	t.Cleanup(func() {
		if err := conn.Exec(ctx, "DELETE FROM telemetry_raw WHERE machine_id = ?", machineID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	want := contract.Telemetry{
		SchemaVersion: contract.SchemaVersion,
		MachineID:     machineID,
		BootID:        4294967295,
		Seq:           1042,
		TS:            time.Date(2026, 11, 7, 14, 3, 0, 123_000_000, time.UTC),
		State:         contract.StateStopped,
		GoodTotal:     812,
		ScrapTotal:    9,
		TemperatureC:  31.5,
		VibrationRMS:  0.42,
	}
	// The same reading twice, as after a redelivery that escaped deduplication.
	if err := insertRows(ctx, conn, []contract.Telemetry{want, want}); err != nil {
		t.Fatal(err)
	}

	var got contract.Telemetry
	var state string
	var count uint64
	row := conn.QueryRow(ctx, `SELECT machine_id, boot_id, seq, ts, state, good_total, scrap_total,
		temperature_c, vibration_rms, count() OVER () FROM telemetry_raw FINAL WHERE machine_id = ?`, machineID)
	err = row.Scan(&got.MachineID, &got.BootID, &got.Seq, &got.TS, &state, &got.GoodTotal, &got.ScrapTotal,
		&got.TemperatureC, &got.VibrationRMS, &count)
	if err != nil {
		t.Fatal(err)
	}
	got.SchemaVersion, got.State = contract.SchemaVersion, contract.State(state)
	if count != 1 {
		t.Errorf("FINAL returned %d rows, want 1", count)
	}
	if !got.TS.Equal(want.TS) {
		t.Errorf("ts = %v, want %v", got.TS, want.TS)
	}
	got.TS = want.TS
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
