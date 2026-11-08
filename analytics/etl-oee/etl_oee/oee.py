"""OEE of one machine over a period, with the rules of docs/adr/0009-regras-do-oee.md.

The Python twin of the Go package internal/oee. Both run the cases of
contracts/oee/cases.json, so the two implementations cannot drift apart.

OEE = availability x performance x quality, each factor limited to [0, 1].
"""

from dataclasses import dataclass
from datetime import datetime, timedelta

RUNNING = "running"


@dataclass(frozen=True)
class Reading:
    """The part of a telemetry reading the calculation uses."""

    boot_id: int
    ts: datetime
    state: str
    good_total: int
    scrap_total: int


@dataclass(frozen=True)
class Result:
    planned: timedelta  # length of the period
    running: timedelta  # time the machine was running
    good: int  # good pieces made in the period
    scrap: int  # scrapped pieces made in the period
    availability: float  # running / planned
    performance: float  # pieces x ideal cycle / running
    quality: float  # good / pieces
    oee: float


def compute(readings: list[Reading], planned: timedelta, ideal_cycle: timedelta) -> Result:
    """Returns the OEE of one machine.

    readings are the machine's readings inside the period, sorted by ts; the
    first one is only the baseline for the next. A reading holds until the next
    one: the interval between them counts as running when the earlier reading
    is running and both share the boot_id.
    """
    running = timedelta()
    good = scrap = 0
    for prev, cur in zip(readings, readings[1:]):
        same_boot = cur.boot_id == prev.boot_id
        # Across a reboot the machine was off for some unknown part of the interval.
        if same_boot and prev.state == RUNNING:
            running += cur.ts - prev.ts
        good += _increase(prev.good_total, cur.good_total, same_boot)
        scrap += _increase(prev.scrap_total, cur.scrap_total, same_boot)
    pieces = good + scrap
    availability = _ratio(running.total_seconds(), planned.total_seconds())
    performance = _ratio(pieces * ideal_cycle.total_seconds(), running.total_seconds())
    quality = _ratio(good, pieces)
    return Result(planned, running, good, scrap, availability, performance, quality,
                  availability * performance * quality)


def _increase(prev: int, cur: int, same_boot: bool) -> int:
    """How much a cumulative counter grew between two readings. After a reboot,
    or if the value goes down, the counter restarted from zero, so its new value
    is the increase and pieces are never negative."""
    if not same_boot or cur < prev:
        return cur
    return cur - prev


def _ratio(num: float, den: float) -> float:
    """Divides and limits the result to [0, 1]. With nothing to divide by, such
    as no running time or no pieces, the factor is 0."""
    if den <= 0:
        return 0.0
    return min(max(num / den, 0.0), 1.0)
