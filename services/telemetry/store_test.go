package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
)

// The tests in this file need the databases of the compose stack, with migrations applied:
//
//	CLICKHOUSE_TEST_DSN=clickhouse://industrial:industrial@localhost:9000/industrial \
//	POSTGRES_TEST_DSN=postgres://industrial:industrial@localhost:5432/industrial?sslmode=disable \
//	go test ./services/telemetry

// testMachine connects to ClickHouse and returns a machine_id of its own,
// whose rows are deleted when the test ends.
func testMachine(t *testing.T) (driver.Conn, string) {
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
	// Cleanups run in reverse order, so the rows are deleted before the connection closes.
	t.Cleanup(func() { conn.Close() })
	machineID := fmt.Sprintf("test-%08x", rand.Uint32())
	t.Cleanup(func() {
		if err := conn.Exec(context.Background(), "DELETE FROM telemetry_raw WHERE machine_id = ?", machineID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return conn, machineID
}

func TestInsertRows(t *testing.T) {
	conn, machineID := testMachine(t)
	ctx := context.Background()
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
	err := row.Scan(&got.MachineID, &got.BootID, &got.Seq, &got.TS, &state, &got.GoodTotal, &got.ScrapTotal,
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

func TestReadingQueries(t *testing.T) {
	conn, machineID := testMachine(t)
	ctx := context.Background()
	s := dbStore{ch: conn}

	// Five readings 400 ms apart, so a period bound rounded to the second
	// would take or leave the wrong ones. The last one is stopped and hotter.
	start := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	ts := func(i int) time.Time { return start.Add(time.Duration(i) * 400 * time.Millisecond) }
	reading := func(i int) contract.Telemetry {
		r := contract.Telemetry{SchemaVersion: contract.SchemaVersion, MachineID: machineID, BootID: 7, Seq: uint64(i),
			TS: ts(i), State: contract.StateRunning, GoodTotal: uint64(i), TemperatureC: 40, VibrationRMS: 0.5}
		if i == 4 {
			r.State, r.TemperatureC, r.VibrationRMS = contract.StateStopped, 50.5, 0.02
		}
		return r
	}
	// Two inserts make two parts, so rows come back unsorted without ORDER BY.
	for _, batch := range [][]int{{2, 3}, {4, 0, 1}} {
		var rows []contract.Telemetry
		for _, i := range batch {
			rows = append(rows, reading(i))
		}
		if err := insertRows(ctx, conn, rows); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("readings in [from, to), sorted by ts", func(t *testing.T) {
		got, err := s.Readings(ctx, machineID, ts(1), ts(3))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || !got[0].TS.Equal(ts(1)) || !got[1].TS.Equal(ts(2)) || got[1].GoodTotal != 2 {
			t.Errorf("got %+v, want the readings at %v and %v", got, ts(1), ts(2))
		}
	})
	t.Run("latest reading", func(t *testing.T) {
		last, err := s.LastReadings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := last[machineID]
		want := lastReading{TS: ts(4), State: contract.StateStopped, TemperatureC: 50.5, VibrationRMS: 0.02}
		if !ok || !got.TS.Equal(want.TS) || got.State != want.State ||
			got.TemperatureC != want.TemperatureC || got.VibrationRMS != want.VibrationRMS {
			t.Errorf("got %+v (found %v), want %+v", got, ok, want)
		}
	})
}

func TestMachineRegistry(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	s := dbStore{pg: pg}

	// The development seed, 0002_seed_machines.sql.
	machines, err := s.Machines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range machines {
		got = append(got, fmt.Sprintf("%s %s", m.ID, m.IdealCycle))
	}
	if want := "[maq-01 4s maq-02 6s maq-03 2.5s maq-04 5s]"; fmt.Sprint(got) != want {
		t.Errorf("got %v, want %s", got, want)
	}

	m, err := s.Machine(ctx, "maq-03")
	if err != nil || m.Name != "Torno CNC" || m.IdealCycle != 2500*time.Millisecond {
		t.Errorf("got %+v, %v; want maq-03, Torno CNC, 2.5s", m, err)
	}
	if _, err := s.Machine(ctx, "maq-99"); !errors.Is(err, errMachineNotFound) {
		t.Errorf("unknown machine: got %v, want errMachineNotFound", err)
	}
}

func TestShifts(t *testing.T) {
	conn, machineID := testMachine(t)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := conn.Exec(context.Background(), "DELETE FROM oee_turno WHERE machine_id = ?", machineID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	start := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	insert := func(date, shift string, start time.Time, oee float64, computedAt time.Time) {
		t.Helper()
		err := conn.Exec(ctx, `INSERT INTO oee_turno (machine_id, shift_date, shift, shift_start, shift_end,
			planned_s, running_s, good, scrap, availability, performance, quality, oee, computed_at)
			VALUES (?, ?, ?, ?, ?, 28800, 14400, 3000, 60, 0.5, 0.85, 0.98, ?, ?)`,
			machineID, date, shift, start, start.Add(8*time.Hour), oee, computedAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	// The 14-22 shift first, and the 06-14 shift computed twice: FINAL keeps the latest.
	insert("2026-11-08", "14-22", start.Add(8*time.Hour), 0.3, start.Add(16*time.Hour))
	insert("2026-11-08", "06-14", start, 0.1, start.Add(8*time.Hour))
	insert("2026-11-08", "06-14", start, 0.2, start.Add(9*time.Hour))
	insert("2026-11-09", "06-14", start.Add(24*time.Hour), 0.9, start.Add(32*time.Hour))

	got, err := dbStore{ch: conn}.Shifts(ctx, machineID, "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, sh := range got {
		summary = append(summary, fmt.Sprintf("%s %s %v %v", sh.Shift, sh.Start.UTC().Format(time.RFC3339), sh.OEE, sh.Good))
	}
	want := "[06-14 2026-11-08T09:00:00Z 0.2 3000 14-22 2026-11-08T17:00:00Z 0.3 3000]"
	if fmt.Sprint(summary) != want {
		t.Errorf("got %v, want %s", summary, want)
	}
}
