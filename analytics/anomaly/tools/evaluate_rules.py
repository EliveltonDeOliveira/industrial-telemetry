"""Measures anomaly rule variants on real readings: the analysis behind ADR 0012.

Export the readings from ClickHouse, running from deploy/:

    docker compose exec -T clickhouse clickhouse-client -u industrial --password industrial \\
      -d industrial --query "SELECT machine_id, toString(ts) AS ts, toString(state) AS state,
      temperature_c, vibration_rms FROM telemetry_raw FINAL WHERE machine_id LIKE 'maq-%'
      ORDER BY ts, machine_id FORMAT JSONEachRow" </dev/null > readings.jsonl

Then, from analytics/anomaly:

    python tools/evaluate_rules.py readings.jsonl --until 2026-11-08T16:24:47

--until leaves out the faults injected with POST /faults, which would count as false alerts.

For each variant it prints the false alerts per machine-hour on the real readings,
with the cooldown, and how many synthetic faults it detects. The faults copy
POST /faults on top of the running readings: temperature approaching 35 C above
normal with a 3 minute time constant, or vibration 4 times higher, for 120 s.
Last, it runs the production Detector over the same readings, which should give
the same false alerts as the chosen variant.
"""

import argparse
import json
import math
import random
import statistics
import sys
from collections import defaultdict, deque
from dataclasses import dataclass, replace
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from anomaly.contract import Telemetry  # noqa: E402
from anomaly.detector import Detector  # noqa: E402

Z = 3.0
COOLDOWN_S = 600.0
FAULT_S = 120.0
WARM_UP = 700  # readings before each synthetic fault, enough to fill a window of 600
MAX_GAP_S = 5.0  # a larger gap between readings is a pause in the data
SENSORS = (("temperatura_alta", "temperature_c"), ("vibracao_alta", "vibration_rms"))


@dataclass(frozen=True)
class Reading:
    machine_id: str
    t: float  # seconds since the epoch
    state: str
    temperature_c: float
    vibration_rms: float


@dataclass(frozen=True)
class Variant:
    name: str
    window: int = 60
    consecutive: int = 3
    two_sided: bool = False
    running_only: bool = True
    exclude_hits: bool = False


VARIANTS = [
    Variant("|z| > 3, all readings, 1 reading (plan)", consecutive=1, two_sided=True, running_only=False),
    Variant("z > 3, running only, 1 reading", consecutive=1),
    Variant("z > 3, running only, 3 in a row (chosen)"),
    Variant("chosen, readings above kept out of the window", exclude_hits=True),
    Variant("chosen, window of 300", window=300),
    Variant("chosen, window of 600", window=600),
]


def alert_times(rows, field, v: Variant, cooldown_s: float):
    """Yields the time of each alert of one sensor of one machine under variant v.

    A model of the production rule with switches for the variants. The window keeps
    running sums, so a window of 600 costs the same as one of 60.
    """
    window = deque()
    total = total_sq = 0.0
    streak = 0
    last = -math.inf
    for r in rows:
        if v.running_only and r.state != "running":
            streak = 0
            continue
        x = getattr(r, field)
        hit = False
        if len(window) == v.window:
            mean = total / v.window
            sd = math.sqrt(max(total_sq / v.window - mean * mean, 0.0))
            if sd > 1e-9:  # below it, a flat window seen through rounding errors
                z = (x - mean) / sd
                hit = abs(z) > Z if v.two_sided else z > Z
        streak = streak + 1 if hit else 0
        if not (hit and v.exclude_hits):
            window.append(x)
            total += x
            total_sq += x * x
            if len(window) > v.window:
                old = window.popleft()
                total -= old
                total_sq -= old * old
        if streak >= v.consecutive and r.t - last >= cooldown_s:
            last = r.t
            yield r.t


def with_fault(rows, field, start):
    """The rows with a fault like POST /faults from start on, for FAULT_S seconds."""
    out = []
    for r in rows:
        dt = r.t - start
        if 0 <= dt < FAULT_S and r.state == "running":
            if field == "temperature_c":
                r = replace(r, temperature_c=r.temperature_c + 35 * (1 - math.exp(-dt / 180)))
            else:
                r = replace(r, vibration_rms=r.vibration_rms * 4)
        out.append(r)
    return out


def synthetic_starts(by_machine, n, seed):
    """Up to n fault starts at random running readings with no pause in the data around them."""
    rng = random.Random(seed)
    trials = []
    for _ in range(n):
        machine = rng.choice(sorted(by_machine))
        rows = by_machine[machine]
        i = rng.randrange(WARM_UP, len(rows) - 200)
        part = rows[i - WARM_UP:i + 150]
        if rows[i].state == "running" and all(b.t - a.t < MAX_GAP_S for a, b in zip(part, part[1:])):
            trials.append((part, rows[i].t))
    return trials


def detection(trials, field, v: Variant):
    """Share of the faults detected within FAULT_S and the median delay in seconds."""
    delays = []
    for part, start in trials:
        for t in alert_times(with_fault(part, field, start), field, v, cooldown_s=0):
            if start <= t < start + FAULT_S:
                delays.append(t - start)
                break
    return len(delays) / len(trials), statistics.median(delays) if delays else math.nan


def machine_hours(by_machine):
    return sum(b.t - a.t for rows in by_machine.values() for a, b in zip(rows, rows[1:]) if b.t - a.t < MAX_GAP_S) / 3600


def production_alerts(by_machine):
    """Sensor alerts of the production Detector with its default rules, by type."""
    detector = Detector()
    counts = defaultdict(int)
    for rows in by_machine.values():
        for seq, r in enumerate(rows):
            t = Telemetry(1, r.machine_id, 1, seq, datetime.fromtimestamp(r.t, timezone.utc), r.state,
                          0, 0, r.temperature_c, r.vibration_rms)
            for alert in detector.process(t):
                counts[alert.type] += 1
    return counts


def parse_time(s: str) -> float:
    ts = datetime.fromisoformat(s)
    return (ts if ts.tzinfo else ts.replace(tzinfo=timezone.utc)).timestamp()


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("readings", help="JSON lines exported from telemetry_raw")
    parser.add_argument("--until", help="only readings before this time, in UTC")
    parser.add_argument("--trials", type=int, default=200, help="synthetic faults to try per sensor")
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    until = parse_time(args.until) if args.until else math.inf
    by_machine = defaultdict(list)
    with open(args.readings) as f:
        for line in f:
            row = json.loads(line)
            r = Reading(row["machine_id"], parse_time(row["ts"]), row["state"],
                        float(row["temperature_c"]), float(row["vibration_rms"]))
            if r.t < until:
                by_machine[r.machine_id].append(r)

    hours = machine_hours(by_machine)
    trials = synthetic_starts(by_machine, args.trials, args.seed)
    print(f"{hours:.1f} machine-hours of readings, {len(trials)} synthetic faults per sensor\n")
    print(f"| {'variant':<46} | false/h temp | false/h vib | detected temp (median s) | detected vib (median s) |")
    print(f"| {'-' * 46} | ---: | ---: | ---: | ---: |")
    for v in VARIANTS:
        false = [sum(1 for rows in by_machine.values() for _ in alert_times(rows, field, v, COOLDOWN_S)) / hours
                 for _, field in SENSORS]
        found = [detection(trials, field, v) for _, field in SENSORS]
        print(f"| {v.name:<46} | {false[0]:.2f} | {false[1]:.2f} | "
              f"{found[0][0]:.0%} ({found[0][1]:.0f}) | {found[1][0]:.0%} ({found[1][1]:.0f}) |")

    counts = production_alerts(by_machine)
    print(f"\nProduction Detector, false alerts per machine-hour: temperature "
          f"{counts['temperatura_alta'] / hours:.2f}, vibration {counts['vibracao_alta'] / hours:.2f}")


if __name__ == "__main__":
    main()
