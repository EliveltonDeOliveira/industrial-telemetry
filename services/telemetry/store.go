package main

import (
	"context"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/EliveltonDeOliveira/industrial-telemetry/internal/contract"
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
