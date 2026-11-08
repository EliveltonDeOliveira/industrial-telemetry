package main

import (
	"context"
	"errors"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/oee"
)

// insertRows writes readings to telemetry_raw in a single INSERT.
func insertRows(ctx context.Context, conn driver.Conn, rows []contract.Telemetry) error {
	batch, err := conn.PrepareBatch(ctx, `INSERT INTO telemetry_raw
		(machine_id, boot_id, seq, ts, state, good_total, scrap_total, temperature_c, vibration_rms)`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, t := range rows {
		err := batch.Append(t.MachineID, t.BootID, t.Seq, t.TS, string(t.State),
			t.GoodTotal, t.ScrapTotal, t.TemperatureC, t.VibrationRMS)
		if err != nil {
			return err
		}
	}
	return batch.Send()
}

var errMachineNotFound = errors.New("machine not found")

// machine is an entry of the machine registry in PostgreSQL.
type machine struct {
	ID         string
	Name       string
	IdealCycle time.Duration // time one piece should take
}

// lastReading is the latest known state of a machine.
type lastReading struct {
	TS           time.Time      `json:"ts"`
	State        contract.State `json:"state"`
	TemperatureC float64        `json:"temperature_c"`
	VibrationRMS float64        `json:"vibration_rms"`
}

// dbStore reads the machine registry from PostgreSQL and readings from ClickHouse.
type dbStore struct {
	pg *pgxpool.Pool
	ch driver.Conn
}

func (s dbStore) Machines(ctx context.Context) ([]machine, error) {
	rows, err := s.pg.Query(ctx, "SELECT machine_id, name, ideal_cycle_s FROM machines ORDER BY machine_id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMachine)
}

// Machine returns errMachineNotFound for a machine that is not registered.
func (s dbStore) Machine(ctx context.Context, id string) (machine, error) {
	rows, err := s.pg.Query(ctx, "SELECT machine_id, name, ideal_cycle_s FROM machines WHERE machine_id = $1", id)
	if err != nil {
		return machine{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, scanMachine)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine{}, errMachineNotFound
	}
	return m, err
}

func scanMachine(row pgx.CollectableRow) (machine, error) {
	var m machine
	var idealCycleS float64
	err := row.Scan(&m.ID, &m.Name, &idealCycleS)
	m.IdealCycle = time.Duration(idealCycleS * float64(time.Second))
	return m, err
}

// LastReadings returns the latest reading of every machine that has one.
// It scans all of telemetry_raw, which is fast at this project's volume; a
// table kept up to date by a materialized view would avoid that if it grows.
func (s dbStore) LastReadings(ctx context.Context) (map[string]lastReading, error) {
	rows, err := s.ch.Query(ctx, `SELECT machine_id, max(ts), argMax(state, ts),
		argMax(temperature_c, ts), argMax(vibration_rms, ts)
		FROM telemetry_raw GROUP BY machine_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	last := map[string]lastReading{}
	for rows.Next() {
		var id, state string
		var r lastReading
		if err := rows.Scan(&id, &r.TS, &state, &r.TemperatureC, &r.VibrationRMS); err != nil {
			return nil, err
		}
		r.State = contract.State(state)
		last[id] = r
	}
	return last, rows.Err()
}

// Readings returns the readings of a machine in [from, to), sorted as the OEE
// calculation needs. Repeated rows not merged yet do not change the result,
// so the query skips FINAL (ADR 0009).
func (s dbStore) Readings(ctx context.Context, machineID string, from, to time.Time) ([]oee.Reading, error) {
	// Positional parameters round times to the second, and ts has milliseconds.
	rows, err := s.ch.Query(ctx, `SELECT boot_id, ts, state, good_total, scrap_total
		FROM telemetry_raw WHERE machine_id = @machine_id AND ts >= @from AND ts < @to
		ORDER BY ts, boot_id, seq`,
		clickhouse.Named("machine_id", machineID),
		clickhouse.DateNamed("from", from, clickhouse.MilliSeconds),
		clickhouse.DateNamed("to", to, clickhouse.MilliSeconds))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var readings []oee.Reading
	for rows.Next() {
		var r oee.Reading
		var state string
		if err := rows.Scan(&r.BootID, &r.TS, &state, &r.GoodTotal, &r.ScrapTotal); err != nil {
			return nil, err
		}
		r.State = contract.State(state)
		readings = append(readings, r)
	}
	return readings, rows.Err()
}

// shiftOEE is the OEE of a machine in one shift, written by etl-oee to oee_turno (ADR 0016).
type shiftOEE struct {
	Shift        string    `json:"shift"` // such as "06-14", in the local hours of the plant
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	PlannedS     float64   `json:"planned_s"`
	RunningS     float64   `json:"running_s"`
	Good         uint64    `json:"good"`
	Scrap        uint64    `json:"scrap"`
	Availability float64   `json:"availability"`
	Performance  float64   `json:"performance"`
	Quality      float64   `json:"quality"`
	OEE          float64   `json:"oee"`
	ComputedAt   time.Time `json:"computed_at"`
}

// Shifts returns the shifts of a machine that start on a local date, in
// order. FINAL keeps only the latest computation of each shift, since the ETL
// rewrites the shifts it covers every hour.
func (s dbStore) Shifts(ctx context.Context, machineID, date string) ([]shiftOEE, error) {
	rows, err := s.ch.Query(ctx, `SELECT shift, shift_start, shift_end, planned_s, running_s, good, scrap,
		availability, performance, quality, oee, computed_at
		FROM oee_turno FINAL WHERE machine_id = @machine_id AND shift_date = @date
		ORDER BY shift_start`,
		clickhouse.Named("machine_id", machineID), clickhouse.Named("date", date))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shifts []shiftOEE
	for rows.Next() {
		var sh shiftOEE
		err := rows.Scan(&sh.Shift, &sh.Start, &sh.End, &sh.PlannedS, &sh.RunningS, &sh.Good, &sh.Scrap,
			&sh.Availability, &sh.Performance, &sh.Quality, &sh.OEE, &sh.ComputedAt)
		if err != nil {
			return nil, err
		}
		shifts = append(shifts, sh)
	}
	return shifts, rows.Err()
}
