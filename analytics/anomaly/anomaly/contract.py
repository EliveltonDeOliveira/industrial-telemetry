"""Messages exchanged with the other services, validated by hand like internal/contract in Go.

The JSON Schemas and examples in contracts/ are the source of truth. The tests read
them, so this module cannot drift from the documented contract or from the Go code.
Every field is required and null counts as missing. Unknown fields are ignored.
"""

import json
import math
import re
from dataclasses import asdict, dataclass
from datetime import datetime, timezone

SCHEMA_VERSION = 1

# Kafka topics, the same as internal/contract/topics.go.
TOPIC_TELEMETRY = "telemetria.bruta"
TOPIC_ALERTS = "alertas"
TOPIC_DLQ = "telemetria.dlq"

# Patterns are used with re.fullmatch, so "$" cannot match before a trailing newline.
MACHINE_ID_PATTERN = r"^[a-z0-9-]{1,64}$"
UUID_PATTERN = r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
# RFC 3339 with a time zone. datetime.fromisoformat alone accepts other ISO 8601 forms.
TS_PATTERN = r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})"

STATES = ("running", "stopped")
ALERT_TYPES = ("temperatura_alta", "vibracao_alta", "parada_longa")
SEVERITIES = ("baixa", "media", "alta")

# Plausible sensor ranges. A reading outside them is a sensor fault, not a measurement.
MIN_TEMPERATURE_C = -40.0
MAX_TEMPERATURE_C = 125.0
MAX_VIBRATION_RMS = 16.0

MAX_UINT32 = 2**32 - 1
MAX_UINT64 = 2**64 - 1


class ContractError(ValueError):
    """The message breaks the contract. Its text goes to the "error" header of the DLQ."""


class FieldError(ContractError):
    """Names the field that made a message invalid, as in "seq: missing"."""

    def __init__(self, field: str, reason: str):
        super().__init__(f"{field}: {reason}")
        self.field = field


@dataclass(frozen=True)
class Telemetry:
    """One machine reading. seq and the counters restart when boot_id changes."""

    schema_version: int
    machine_id: str
    boot_id: int
    seq: int
    ts: datetime
    state: str
    good_total: int
    scrap_total: int
    temperature_c: float
    vibration_rms: float


@dataclass(frozen=True)
class Alert:
    """An anomaly detected on a machine. value and threshold use the unit of the type."""

    schema_version: int
    alert_id: str
    machine_id: str
    ts: datetime
    type: str
    severity: str
    value: float
    threshold: float

    def to_json(self) -> bytes:
        fields = asdict(self)
        fields["ts"] = format_ts(self.ts)
        return json.dumps(fields).encode()


def parse_telemetry(data: bytes) -> Telemetry:
    """Decodes and validates a telemetry message. The timestamp is returned in UTC."""
    t = Telemetry(**_decode(data, {
        "schema_version": _int(),
        "machine_id": _str,
        "boot_id": _int(MAX_UINT32),
        "seq": _int(),
        "ts": _ts,
        "state": _str,
        "good_total": _int(),
        "scrap_total": _int(),
        "temperature_c": _number,
        "vibration_rms": _number,
    }))
    validate_telemetry(t)
    return t


def validate_telemetry(t: Telemetry) -> None:
    """Checks the rules that the types alone do not enforce, in the same order as Go."""
    _check_version(t.schema_version)
    _check_machine_id(t.machine_id)
    _check_ts(t.ts)
    if t.state not in STATES:
        raise FieldError("state", f"{t.state!r} is not one of {list(STATES)}")
    # Written as "not inside" so NaN, which fails every comparison, is rejected too.
    if not MIN_TEMPERATURE_C <= t.temperature_c <= MAX_TEMPERATURE_C:
        raise FieldError("temperature_c", f"{t.temperature_c} is outside [{MIN_TEMPERATURE_C:g}, {MAX_TEMPERATURE_C:g}]")
    if not 0 <= t.vibration_rms <= MAX_VIBRATION_RMS:
        raise FieldError("vibration_rms", f"{t.vibration_rms} is outside [0, {MAX_VIBRATION_RMS:g}]")


def parse_alert(data: bytes) -> Alert:
    """Decodes and validates an alert message. The timestamp is returned in UTC."""
    a = Alert(**_decode(data, {
        "schema_version": _int(),
        "alert_id": _str,
        "machine_id": _str,
        "ts": _ts,
        "type": _str,
        "severity": _str,
        "value": _number,
        "threshold": _number,
    }))
    validate_alert(a)
    return a


def validate_alert(a: Alert) -> None:
    """Checks the rules that the types alone do not enforce. Called before publishing."""
    _check_version(a.schema_version)
    if not re.fullmatch(UUID_PATTERN, a.alert_id):
        raise FieldError("alert_id", f"{a.alert_id!r} is not a lowercase UUID")
    _check_machine_id(a.machine_id)
    _check_ts(a.ts)
    if a.type not in ALERT_TYPES:
        raise FieldError("type", f"{a.type!r} is not one of {list(ALERT_TYPES)}")
    if a.severity not in SEVERITIES:
        raise FieldError("severity", f"{a.severity!r} is not one of {list(SEVERITIES)}")
    if not math.isfinite(a.value):
        raise FieldError("value", f"{a.value} is not a finite number")
    if not math.isfinite(a.threshold):
        raise FieldError("threshold", f"{a.threshold} is not a finite number")


def format_ts(ts: datetime) -> str:
    """RFC 3339 in UTC with milliseconds, as the Go services write it."""
    return ts.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _check_version(schema_version: int) -> None:
    if schema_version != SCHEMA_VERSION:
        raise FieldError("schema_version", f"unsupported version {schema_version}")


def _check_machine_id(machine_id: str) -> None:
    if not re.fullmatch(MACHINE_ID_PATTERN, machine_id):
        raise FieldError("machine_id", f"{machine_id!r} does not match {MACHINE_ID_PATTERN}")


def _check_ts(ts: datetime) -> None:
    # The zero time of Go, which a producer sends when it forgets to fill ts.
    if ts == datetime.min.replace(tzinfo=timezone.utc):
        raise FieldError("ts", "missing")


def _decode(data: bytes, fields: dict) -> dict:
    """Decodes a JSON object one field at a time, so an error names the field.

    Like Go, every field is decoded first and only then validated.
    """
    try:
        # NaN and Infinity are not JSON, though Python accepts them by default.
        obj = json.loads(data, parse_constant=_reject_constant)
    except (TypeError, ValueError, RecursionError) as e:
        raise ContractError(f"message is not a JSON object: {e}") from None
    if not isinstance(obj, dict):
        raise ContractError(f"message is not a JSON object: got {type(obj).__name__}")
    out = {}
    for name, decode in fields.items():
        value = obj.get(name)
        if value is None:
            raise FieldError(name, "missing")
        out[name] = decode(name, value)
    return out


def _reject_constant(name: str):
    raise ValueError(f"{name} is not valid JSON")


def _int(maximum: int = MAX_UINT64):
    """An unsigned integer. 1042.0 and true are rejected, as in Go."""
    def decode(name, value):
        if isinstance(value, bool) or not isinstance(value, int):
            raise FieldError(name, f"{value!r} is not an integer")
        if not 0 <= value <= maximum:
            raise FieldError(name, f"{value} is outside [0, {maximum}]")
        return value
    return decode


def _number(name, value) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise FieldError(name, f"{value!r} is not a number")
    try:
        return float(value)
    except OverflowError:
        raise FieldError(name, f"{value} is out of range") from None


def _str(name, value) -> str:
    if not isinstance(value, str):
        raise FieldError(name, f"{value!r} is not a string")
    return value


def _ts(name, value) -> datetime:
    if not isinstance(value, str) or not re.fullmatch(TS_PATTERN, value):
        raise FieldError(name, f"{value!r} is not RFC 3339 with a time zone")
    try:
        return datetime.fromisoformat(value).astimezone(timezone.utc)
    except (ValueError, OverflowError) as e:
        raise FieldError(name, f"{value!r}: {e}") from None
