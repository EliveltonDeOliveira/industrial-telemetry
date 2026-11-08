"""Shifts of the plant, defined in local hours and turned into UTC periods."""

import re
from dataclasses import dataclass
from datetime import date, datetime, time, timedelta, timezone, tzinfo


@dataclass(frozen=True)
class ShiftDef:
    label: str  # such as "06-14", stored in oee_turno.shift
    start_hour: int
    end_hour: int  # on the next day when it is not after start_hour


@dataclass(frozen=True)
class Shift:
    label: str
    date: date  # local date the shift starts on
    start: datetime  # UTC
    end: datetime  # UTC


def parse_shifts(text: str) -> list[ShiftDef]:
    """Parses ETL_SHIFTS, such as "06-14,14-22,22-06".

    Shifts may leave hours out, which are then not planned time, but must not
    overlap. A shift whose end is not after its start ends on the next day.
    """
    defs, covered = [], set()
    for part in (p.strip() for p in text.split(",")):
        m = re.fullmatch(r"(\d{2})-(\d{2})", part)
        if not m:
            raise ValueError(f"ETL_SHIFTS: {part!r} is not HH-HH, such as 06-14")
        start, end = int(m[1]), int(m[2])
        if start > 23 or end > 23 or start == end:
            raise ValueError(f"ETL_SHIFTS: {part} needs two different hours from 00 to 23")
        hours = {(start + i) % 24 for i in range((end - start) % 24)}
        if hours & covered:
            raise ValueError(f"ETL_SHIFTS: {part} overlaps another shift")
        covered |= hours
        defs.append(ShiftDef(part, start, end))
    return sorted(defs, key=lambda d: d.start_hour)


def ended_between(defs: list[ShiftDef], tz: tzinfo, since: datetime, until: datetime) -> list[Shift]:
    """The shifts that ended in (since, until], oldest first.

    Boundaries are local hours in tz, so on a daylight saving change a shift
    lasts an hour more or less, and its planned time follows.
    """
    shifts = []
    day = since.astimezone(tz).date() - timedelta(days=1)
    while day <= until.astimezone(tz).date():
        for d in defs:
            end_day = day if d.end_hour > d.start_hour else day + timedelta(days=1)
            start = datetime.combine(day, time(d.start_hour), tz).astimezone(timezone.utc)
            end = datetime.combine(end_day, time(d.end_hour), tz).astimezone(timezone.utc)
            if since < end <= until:
                shifts.append(Shift(d.label, day, start, end))
        day += timedelta(days=1)
    return sorted(shifts, key=lambda s: s.end)
