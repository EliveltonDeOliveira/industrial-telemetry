import json
from pathlib import Path

from anomaly.contract import TOPIC_ALERTS, TOPIC_DLQ, parse_alert
from anomaly.detector import Config, Detector
from anomaly.service import Batch

CONTRACTS = Path(__file__).resolve().parents[3] / "contracts"
RUNNING = json.loads((CONTRACTS / "examples" / "telemetry" / "valid" / "running.json").read_text())


def message(**changes) -> bytes:
    return json.dumps({**RUNNING, **changes}).encode()


def test_invalid_message_goes_to_the_dlq_unchanged():
    batch = Batch()
    value = message(seq=None)
    batch.add(Detector(), b"maq-01", value)
    [r] = batch.records
    assert (r.topic, r.key, r.value) == (TOPIC_DLQ, b"maq-01", value)
    assert dict(r.headers) == {
        "error": b"seq: missing",
        "source": b"kafka:telemetria.bruta",
        "service": b"anomaly",
    }


def test_tombstone_goes_to_the_dlq():
    batch = Batch()
    batch.add(Detector(), b"maq-01", None)
    assert [r.topic for r in batch.records] == [TOPIC_DLQ]


def test_alerts_are_keyed_by_machine():
    detector = Detector(Config(window=2, consecutive=1))
    batch = Batch()
    for seq, temp in enumerate([30.0, 31.0, 40.0]):
        batch.add(detector, b"maq-01", message(seq=seq, temperature_c=temp))
    [r] = batch.records
    assert (r.topic, r.key, r.alert_type) == (TOPIC_ALERTS, b"maq-01", "temperatura_alta")
    assert parse_alert(r.value).machine_id == "maq-01"
    assert batch.consumed == 3


def test_repeated_reading_is_counted():
    batch = Batch()
    detector = Detector()
    batch.add(detector, b"maq-01", message())
    batch.add(detector, b"maq-01", message())
    assert (batch.consumed, batch.duplicates, batch.records) == (2, 1, [])
