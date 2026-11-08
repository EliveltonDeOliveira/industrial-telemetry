"""Runs the OEE cases shared with the Go package internal/oee (ADR 0009)."""

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

from etl_oee.oee import Reading, compute

CASES = json.loads((Path(__file__).resolve().parents[3] / "contracts/oee/cases.json").read_text())["cases"]
START = datetime(2026, 11, 7, 14, tzinfo=timezone.utc)


def test_there_are_cases():
    assert len(CASES) > 10


@pytest.mark.parametrize("case", CASES, ids=[c["name"] for c in CASES])
def test_compute(case):
    readings = [Reading(r["boot_id"], START + timedelta(seconds=r["t"]), r["state"], r["good_total"], r["scrap_total"])
                for r in case["readings"]]
    got = compute(readings, timedelta(seconds=case["planned_s"]), timedelta(seconds=case["ideal_cycle_s"]))
    want = case["want"]
    assert got.planned == timedelta(seconds=case["planned_s"])
    assert got.running == timedelta(seconds=want["running_s"])
    assert (got.good, got.scrap) == (want["good"], want["scrap"])
    for factor in ("availability", "performance", "quality", "oee"):
        assert getattr(got, factor) == pytest.approx(want[factor], abs=1e-9), factor
