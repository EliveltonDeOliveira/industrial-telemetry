"""Reads the same schemas and examples as internal/contract in Go, so both languages
accept and reject the same messages."""

import json
from datetime import datetime, timezone
from pathlib import Path

import pytest

from anomaly import contract
from anomaly.contract import Alert, ContractError, FieldError, Telemetry, parse_alert, parse_telemetry

CONTRACTS = Path(__file__).resolve().parents[3] / "contracts"

PARSERS = {"telemetry": parse_telemetry, "alert": parse_alert}


def valid_examples():
    for kind in PARSERS:
        for path in sorted((CONTRACTS / "examples" / kind / "valid").glob("*.json")):
            yield pytest.param(kind, path, id=f"{kind}/{path.name}")


def invalid_cases():
    for kind in PARSERS:
        for c in json.loads((CONTRACTS / "examples" / kind / "invalid.json").read_text()):
            yield pytest.param(kind, c["field"], c["message"], id=f"{kind}/{c['case']}")


@pytest.mark.parametrize("kind, path", list(valid_examples()))
def test_valid_examples(kind, path):
    PARSERS[kind](path.read_bytes())


@pytest.mark.parametrize("kind, field, message", list(invalid_cases()))
def test_invalid_cases(kind, field, message):
    with pytest.raises(FieldError) as err:
        PARSERS[kind](json.dumps(message).encode())
    assert err.value.field == field
    # The text goes to the "error" header of the DLQ and starts with the field.
    assert str(err.value).startswith(f"{field}: ")


@pytest.mark.parametrize("file, want", [
    ("running.json", Telemetry(
        schema_version=1, machine_id="maq-01", boot_id=2849120113, seq=1042,
        ts=datetime(2026, 11, 7, 14, 3, tzinfo=timezone.utc), state="running",
        good_total=812, scrap_total=9, temperature_c=31.5, vibration_rms=0.42)),
    ("ts-with-offset.json", Telemetry(
        schema_version=1, machine_id="maq-02", boot_id=1593024788, seq=57,
        ts=datetime(2026, 11, 7, 14, 3, tzinfo=timezone.utc), state="running",
        good_total=40, scrap_total=1, temperature_c=29.8, vibration_rms=0.38)),
])
def test_parse_telemetry(file, want):
    got = parse_telemetry((CONTRACTS / "examples" / "telemetry" / "valid" / file).read_bytes())
    assert got == want
    assert got.ts.tzinfo == timezone.utc


def test_alert_round_trip():
    data = (CONTRACTS / "examples" / "alert" / "valid" / "temperatura-alta.json").read_bytes()
    alert = parse_alert(data)
    assert alert == Alert(
        schema_version=1, alert_id="8f2c1a7e-3b1d-4c6e-9a0f-2d5b7e9c1a44", machine_id="maq-01",
        ts=datetime(2026, 11, 7, 14, 5, tzinfo=timezone.utc), type="temperatura_alta",
        severity="alta", value=78.2, threshold=65.0)
    assert json.loads(alert.to_json())["ts"] == "2026-11-07T14:05:00.000Z"
    assert parse_alert(alert.to_json()) == alert


@pytest.mark.parametrize("data", [b"", b"{", b"[]", b'"maq-01"', b"42", b"null", None, b"\xff"])
def test_rejects_malformed_json(data):
    for parse in PARSERS.values():
        with pytest.raises(ContractError):
            parse(data)


@pytest.mark.parametrize("field, value", [
    ("seq", 1042.0),  # Go decodes integers strictly
    ("seq", True),
    ("seq", 2**64),  # above uint64
    ("boot_id", -1),
    ("temperature_c", "31.5"),
    ("temperature_c", False),
    ("ts", "2026-11-07 14:03:00Z"),  # fromisoformat accepts it, RFC 3339 does not
    ("ts", "20261107T140300Z"),
    ("ts", "2026-11-07T14:03Z"),
    ("ts", "2026-11-07T14:03:00+25:00"),
    ("ts", "0001-01-01T00:00:00Z"),  # the zero time of Go
    ("machine_id", "maq-01\n"),
    ("machine_id", 1),
])
def test_rejects_field(field, value):
    message = json.loads((CONTRACTS / "examples" / "telemetry" / "valid" / "running.json").read_text())
    message[field] = value
    with pytest.raises(FieldError) as err:
        parse_telemetry(json.dumps(message).encode())
    assert err.value.field == field


def test_rejects_non_finite_numbers():
    running = (CONTRACTS / "examples" / "telemetry" / "valid" / "running.json").read_text()
    for literal in ("NaN", "Infinity", "-Infinity"):
        with pytest.raises(ContractError):
            parse_telemetry(running.replace("31.5", literal).encode())
    with pytest.raises(FieldError, match="^temperature_c: "):
        parse_telemetry(running.replace("31.5", "1e400").encode())  # decodes to inf


@pytest.mark.parametrize("field, value", [("value", float("nan")), ("threshold", float("inf"))])
def test_validate_alert_rejects_non_finite(field, value):
    alert = parse_alert((CONTRACTS / "examples" / "alert" / "valid" / "temperatura-alta.json").read_bytes())
    with pytest.raises(FieldError) as err:
        contract.validate_alert(Alert(**{**vars(alert), field: value}))
    assert err.value.field == field


@pytest.mark.parametrize("file, cls, keywords", [
    ("telemetry.schema.json", Telemetry, {
        "schema_version": {"const": contract.SCHEMA_VERSION},
        "machine_id": {"pattern": contract.MACHINE_ID_PATTERN},
        "boot_id": {"minimum": 0, "maximum": contract.MAX_UINT32},
        "state": {"enum": list(contract.STATES)},
        "temperature_c": {"minimum": contract.MIN_TEMPERATURE_C, "maximum": contract.MAX_TEMPERATURE_C},
        "vibration_rms": {"minimum": 0, "maximum": contract.MAX_VIBRATION_RMS},
    }),
    ("alert.schema.json", Alert, {
        "schema_version": {"const": contract.SCHEMA_VERSION},
        "alert_id": {"pattern": contract.UUID_PATTERN},
        "machine_id": {"pattern": contract.MACHINE_ID_PATTERN},
        "type": {"enum": list(contract.ALERT_TYPES)},
        "severity": {"enum": list(contract.SEVERITIES)},
    }),
])
def test_schema_matches_python(file, cls, keywords):
    """The schemas declare the same fields, enums and limits this module enforces."""
    schema = json.loads((CONTRACTS / file).read_text())
    names = sorted(cls.__dataclass_fields__)
    assert sorted(schema["required"]) == names
    assert sorted(schema["properties"]) == names
    for prop, expected in keywords.items():
        for keyword, value in expected.items():
            assert schema["properties"][prop][keyword] == value, f"{prop}.{keyword}"
