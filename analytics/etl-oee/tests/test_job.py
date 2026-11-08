from datetime import date, datetime, timedelta, timezone

import pytest

from etl_oee.job import COLUMNS, Config, Machine, next_run, run
from etl_oee.oee import Reading


def utc(*args):
    return datetime(*args, tzinfo=timezone.utc)


class FakeStore:
    """maq-01 runs the whole shift at the ideal cycle, maq-04 sends nothing."""

    def __init__(self):
        self.asked = []
        self.written = None

    def machines(self):
        return [Machine("maq-01", timedelta(seconds=4)), Machine("maq-04", timedelta(seconds=5))]

    def readings(self, machine_id, start, end):
        self.asked.append((machine_id, start, end))
        if machine_id != "maq-01":
            return []
        return [Reading(7, start, "running", 0, 0), Reading(7, end, "running", (end - start) // timedelta(seconds=4), 0)]

    def write(self, rows):
        self.written = rows


CFG = Config.from_env({})


def test_run():
    store = FakeStore()
    now = utc(2026, 11, 7, 17, 5, 30, 123456)  # 14:05:30 in Sao Paulo
    rows = run(store, CFG, now)

    # The shifts that ended in the 24 h before: 14-22 and 22-06 of the 6th and 06-14 of the 7th.
    assert [(r[0], r[1], r[2]) for r in rows] == [
        ("maq-01", date(2026, 11, 6), "14-22"), ("maq-01", date(2026, 11, 6), "22-06"), ("maq-01", date(2026, 11, 7), "06-14"),
        ("maq-04", date(2026, 11, 6), "14-22"), ("maq-04", date(2026, 11, 6), "22-06"), ("maq-04", date(2026, 11, 7), "06-14"),
    ]
    assert store.written == rows
    assert store.asked[2] == ("maq-01", utc(2026, 11, 7, 9), utc(2026, 11, 7, 17))

    row = dict(zip(COLUMNS, rows[2]))
    assert row == {
        "machine_id": "maq-01", "shift_date": date(2026, 11, 7), "shift": "06-14",
        "shift_start": utc(2026, 11, 7, 9), "shift_end": utc(2026, 11, 7, 17),
        "planned_s": 28800.0, "running_s": 28800.0, "good": 7200, "scrap": 0,
        "availability": 1.0, "performance": 1.0, "quality": 1.0, "oee": 1.0,
        "computed_at": utc(2026, 11, 7, 17, 5, 30),
    }
    # A machine without readings gets zeros, as time without readings is not running (ADR 0009).
    zeros = dict(zip(COLUMNS, rows[5]))
    assert (zeros["planned_s"], zeros["running_s"], zeros["good"], zeros["oee"]) == (28800.0, 0.0, 0, 0.0)


def test_run_without_shifts_writes_nothing():
    store = FakeStore()
    cfg = Config.from_env({"ETL_LOOKBACK_H": "1"})
    assert run(store, cfg, utc(2026, 11, 7, 18, 30)) == []
    assert store.written is None


def test_config_defaults():
    assert str(CFG.tz) == "America/Sao_Paulo"
    assert [d.label for d in CFG.shifts] == ["06-14", "14-22", "22-06"]
    assert CFG.lookback == timedelta(hours=24)
    assert CFG.clickhouse_dsn.startswith("http://") and CFG.postgres_dsn.startswith("postgres://")


@pytest.mark.parametrize("env, var", [
    ({"ETL_TIMEZONE": "Brasil/Campinas"}, "ETL_TIMEZONE"),
    ({"ETL_TIMEZONE": "../etc/passwd"}, "ETL_TIMEZONE"),
    ({"ETL_LOOKBACK_H": "0"}, "ETL_LOOKBACK_H"),
    ({"ETL_LOOKBACK_H": "1.5"}, "ETL_LOOKBACK_H"),
    ({"ETL_LOOKBACK_H": "745"}, "ETL_LOOKBACK_H"),
    ({"ETL_SHIFTS": "06-14,10-18"}, "ETL_SHIFTS"),
])
def test_config_rejects(env, var):
    with pytest.raises(ValueError, match=f"^{var}: "):
        Config.from_env(env)


@pytest.mark.parametrize("now, want", [
    (utc(2026, 11, 7, 14, 0), utc(2026, 11, 7, 14, 5)),
    (utc(2026, 11, 7, 14, 5), utc(2026, 11, 7, 15, 5)),
    (utc(2026, 11, 7, 23, 59, 59), utc(2026, 11, 8, 0, 5)),
])
def test_next_run(now, want):
    assert next_run(now) == want
