"""Reads machines from PostgreSQL and readings from ClickHouse, and writes oee_turno."""

from datetime import datetime, timedelta

import clickhouse_connect
import psycopg

from .job import COLUMNS, Machine
from .oee import Reading


class DBStore:
    """Connects on creation. The ETL creates one per run, so a database that is
    down only fails that run."""

    def __init__(self, clickhouse_dsn: str, postgres_dsn: str):
        self.ch = clickhouse_connect.get_client(dsn=clickhouse_dsn)
        self.postgres_dsn = postgres_dsn

    def close(self) -> None:
        self.ch.close()

    def machines(self) -> list[Machine]:
        with psycopg.connect(self.postgres_dsn) as conn:
            rows = conn.execute("SELECT machine_id, ideal_cycle_s FROM machines ORDER BY machine_id").fetchall()
        return [Machine(machine_id, timedelta(seconds=cycle)) for machine_id, cycle in rows]

    def readings(self, machine_id: str, start: datetime, end: datetime) -> list[Reading]:
        # Repeated rows not merged yet do not change the result, so the query skips FINAL (ADR 0009).
        result = self.ch.query(
            """SELECT boot_id, ts, state, good_total, scrap_total FROM telemetry_raw
            WHERE machine_id = {machine_id:String}
              AND ts >= {start:DateTime64(3, 'UTC')} AND ts < {end:DateTime64(3, 'UTC')}
            ORDER BY ts, boot_id, seq""",
            parameters={"machine_id": machine_id, "start": start, "end": end},
        )
        return [Reading(*row) for row in result.result_rows]

    def write(self, rows: list[tuple]) -> None:
        self.ch.insert("oee_turno", rows, column_names=COLUMNS)
