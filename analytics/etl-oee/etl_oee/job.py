"""One run of the ETL and its configuration."""

from dataclasses import dataclass
from datetime import datetime, timedelta, timezone, tzinfo
from typing import Protocol
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from .oee import Reading, compute
from .shifts import ShiftDef, ended_between, parse_shifts

# Shifts end on the hour; a few minutes later the last readings are stored.
RUN_MINUTE = 5
MAX_LOOKBACK_H = 24 * 31

# Columns of oee_turno written by the ETL, in the order of each row.
COLUMNS = ["machine_id", "shift_date", "shift", "shift_start", "shift_end", "planned_s", "running_s",
           "good", "scrap", "availability", "performance", "quality", "oee", "computed_at"]

ENV_DEFAULTS = {
    "CLICKHOUSE_DSN": "http://industrial:industrial@clickhouse:8123/industrial",
    "POSTGRES_DSN": "postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable",
    "ETL_TIMEZONE": "America/Sao_Paulo",
    "ETL_SHIFTS": "06-14,14-22,22-06",
    "ETL_LOOKBACK_H": "24",
}


@dataclass(frozen=True)
class Machine:
    machine_id: str
    ideal_cycle: timedelta


class Store(Protocol):
    def machines(self) -> list[Machine]: ...

    def readings(self, machine_id: str, start: datetime, end: datetime) -> list[Reading]: ...

    def write(self, rows: list[tuple]) -> None: ...


@dataclass(frozen=True)
class Config:
    clickhouse_dsn: str
    postgres_dsn: str
    tz: tzinfo
    shifts: list[ShiftDef]
    lookback: timedelta

    @classmethod
    def from_env(cls, env) -> "Config":
        """Reads the configuration, with errors that start with the variable name."""
        def get(name):
            return env.get(name) or ENV_DEFAULTS[name]

        try:
            tz = ZoneInfo(get("ETL_TIMEZONE"))
        except (ZoneInfoNotFoundError, ValueError):
            raise ValueError(f"ETL_TIMEZONE: unknown time zone {get('ETL_TIMEZONE')!r}") from None
        lookback = get("ETL_LOOKBACK_H")
        if not lookback.isdigit() or not 1 <= int(lookback) <= MAX_LOOKBACK_H:
            raise ValueError(f"ETL_LOOKBACK_H: {lookback!r} is not a number of hours from 1 to {MAX_LOOKBACK_H}")
        return cls(get("CLICKHOUSE_DSN"), get("POSTGRES_DSN"), tz, parse_shifts(get("ETL_SHIFTS")),
                   timedelta(hours=int(lookback)))


def run(store: Store, cfg: Config, now: datetime) -> list[tuple]:
    """Computes the OEE of every registered machine in every shift that ended
    in the lookback before now, and writes one row per machine and shift.

    Each run rewrites the shifts it covers, so readings that arrive late are
    counted by a later run, and oee_turno keeps the row with the latest
    computed_at. A machine without readings in a shift gets a row with zeros,
    as ADR 0009 counts time without readings as not running.
    """
    shifts = ended_between(cfg.shifts, cfg.tz, now - cfg.lookback, now)
    computed_at = now.astimezone(timezone.utc).replace(microsecond=0)
    rows = []
    for m in store.machines():
        for s in shifts:
            r = compute(store.readings(m.machine_id, s.start, s.end), s.end - s.start, m.ideal_cycle)
            rows.append((m.machine_id, s.date, s.label, s.start, s.end, r.planned.total_seconds(),
                         r.running.total_seconds(), r.good, r.scrap, r.availability, r.performance,
                         r.quality, r.oee, computed_at))
    if rows:
        store.write(rows)
    return rows


def next_run(now: datetime) -> datetime:
    """The next RUN_MINUTE past the hour, in UTC, after now."""
    nxt = now.astimezone(timezone.utc).replace(minute=RUN_MINUTE, second=0, microsecond=0)
    return nxt if nxt > now else nxt + timedelta(hours=1)
