from datetime import date, datetime, timedelta, timezone
from zoneinfo import ZoneInfo

import pytest

from etl_oee.shifts import Shift, ended_between, parse_shifts

SP = ZoneInfo("America/Sao_Paulo")
DEFAULT = parse_shifts("06-14,14-22,22-06")


def utc(*args):
    return datetime(*args, tzinfo=timezone.utc)


def local(*args):
    return datetime(*args, tzinfo=SP)


def test_parse_shifts():
    assert [(d.label, d.start_hour, d.end_hour) for d in DEFAULT] == [("06-14", 6, 14), ("14-22", 14, 22), ("22-06", 22, 6)]
    # Hours left out are not planned time.
    assert [d.label for d in parse_shifts(" 14-22 , 06-14 ")] == ["06-14", "14-22"]


@pytest.mark.parametrize("text", ["", "6-14", "06-14-22", "06:14", "06-24", "06-06", "06-14,12-20", "22-06,05-07"])
def test_parse_shifts_rejects(text):
    with pytest.raises(ValueError, match="^ETL_SHIFTS: "):
        parse_shifts(text)


def test_shifts_of_a_day():
    # Sao Paulo is UTC-3 in 2026. The 22-06 shift starts on the date before.
    got = ended_between(DEFAULT, SP, local(2026, 11, 7, 0, 0), local(2026, 11, 8, 0, 0))
    assert got == [
        Shift("22-06", date(2026, 11, 6), utc(2026, 11, 7, 1), utc(2026, 11, 7, 9)),
        Shift("06-14", date(2026, 11, 7), utc(2026, 11, 7, 9), utc(2026, 11, 7, 17)),
        Shift("14-22", date(2026, 11, 7), utc(2026, 11, 7, 17), utc(2026, 11, 8, 1)),
    ]


def test_window_takes_shifts_that_ended_in_it():
    # A shift that ends exactly at until is in, one that ends exactly at since is out.
    got = ended_between(DEFAULT, SP, local(2026, 11, 7, 14, 0), local(2026, 11, 7, 22, 0))
    assert [s.label for s in got] == ["14-22"]
    # The shift in progress is not in.
    assert ended_between(DEFAULT, SP, local(2026, 11, 7, 14, 0), local(2026, 11, 7, 21, 59)) == []


@pytest.mark.parametrize("start_day, hours", [
    (date(2018, 11, 3), 7),  # clocks went forward at midnight on 2018-11-04
    (date(2019, 2, 16), 9),  # clocks went back at midnight on 2019-02-17
    (date(2026, 11, 7), 8),
])
def test_night_shift_on_daylight_saving_change(start_day, hours):
    since = datetime.combine(start_day, datetime.min.time(), SP)
    night = [s for s in ended_between(DEFAULT, SP, since, since + timedelta(days=2)) if s.date == start_day and s.label == "22-06"]
    assert len(night) == 1 and night[0].end - night[0].start == timedelta(hours=hours)


def test_utc_shifts():
    got = ended_between(parse_shifts("00-12,12-00"), timezone.utc, utc(2026, 11, 7, 0), utc(2026, 11, 8, 0))
    assert [(s.label, s.date, s.start, s.end) for s in got] == [
        ("00-12", date(2026, 11, 7), utc(2026, 11, 7, 0), utc(2026, 11, 7, 12)),
        ("12-00", date(2026, 11, 7), utc(2026, 11, 7, 12), utc(2026, 11, 8, 0)),
    ]
