"""Kafka consumer loop and HTTP server of the anomaly service."""

import json
import logging
import threading
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from confluent_kafka import Consumer, KafkaException, Producer
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest

from .contract import ALERT_TYPES, TOPIC_ALERTS, TOPIC_DLQ, TOPIC_TELEMETRY, ContractError, parse_telemetry
from .detector import Detector

SERVICE = "anomaly"
CONSUMER_GROUP = "anomaly"
MAX_BATCH = 500
BATCH_WAIT_S = 1.0
MAX_RETRY_WAIT_S = 30.0

log = logging.getLogger(SERVICE)

consumed = Counter("anomaly_records_consumed", "Records consumed from telemetria.bruta, committed after they are handled.")
alerts_published = Counter("anomaly_alerts_published", "Alerts published to alertas, by type.", ["type"])
dead_lettered = Counter("anomaly_dead_lettered", "Records sent to telemetria.dlq because they break the contract.")
duplicates = Counter("anomaly_duplicates_dropped", "Readings dropped because their seq was already seen for the same machine_id and boot_id.")
failures = Counter("anomaly_failures", "Failed attempts that are retried, by operation.", ["operation"])
# It includes any difference between the clock of the device, which sets ts, and this one (ADR 0017).
alert_delay = Histogram("anomaly_alert_delay_seconds", "Time from the ts of the reading that fired an alert to publishing it.",
                        buckets=[0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300])
# Every labeled series exists at 0 from the start. A series that first appears at 1
# hides that increment from rate() and increase() (ADR 0018).
for alert_type in ALERT_TYPES:
    alerts_published.labels(alert_type)
for operation in ("produce", "commit"):
    failures.labels(operation)


@dataclass(frozen=True)
class OutRecord:
    topic: str
    key: bytes | None
    value: bytes | None
    headers: list | None = None
    alert_type: str | None = None
    ts: datetime | None = None  # of the reading that fired the alert


@dataclass
class Batch:
    """What one poll of telemetria.bruta turns into. It has no I/O, so it is unit tested."""

    records: list = field(default_factory=list)
    consumed: int = 0
    duplicates: int = 0

    def add(self, detector: Detector, key: bytes | None, value: bytes | None) -> None:
        self.consumed += 1
        try:
            reading = parse_telemetry(value)
        except ContractError as e:
            self.records.append(dead_letter(key, value, e))
            return
        alerts = detector.process(reading)
        if alerts is None:
            self.duplicates += 1
            return
        for a in alerts:
            self.records.append(OutRecord(TOPIC_ALERTS, a.machine_id.encode(), a.to_json(), alert_type=a.type, ts=a.ts))


def dead_letter(key: bytes | None, value: bytes | None, err: Exception) -> OutRecord:
    """The original message, unchanged, with the headers of ADR 0008."""
    return OutRecord(TOPIC_DLQ, key, value, headers=[
        ("error", str(err).encode()),
        ("source", f"kafka:{TOPIC_TELEMETRY}".encode()),
        ("service", SERVICE.encode()),
    ])


class Service:
    def __init__(self, brokers: str, detector: Detector):
        self.detector = detector
        self.stopping = threading.Event()
        self.consumer = Consumer({
            "bootstrap.servers": brokers,
            "group.id": CONSUMER_GROUP,
            "enable.auto.commit": False,
            # A new consumer group starts at the end of the topic: alerts are about
            # now, and replaying days of history would open old work orders.
            "auto.offset.reset": "latest",
        })
        self.producer = Producer({
            "bootstrap.servers": brokers,
            "enable.idempotence": True,
            # The same hash as the Java client and franz-go, so a machine_id lands on
            # the same partition whatever the language of the producer. The librdkafka
            # default, consistent_random, uses CRC32 and would send it elsewhere.
            "partitioner": "murmur2_random",
            # Bounds each attempt, so a broker that is down shows up as a failure and a retry.
            "message.timeout.ms": 30000,
        })

    def run(self) -> None:
        """Consumes until stopping is set. Offsets are committed only after the alerts
        and dead letters of the batch are written, so delivery is at least once.
        Rebalances only happen inside consume, so partitions move between batches."""
        self.consumer.subscribe([TOPIC_TELEMETRY])
        try:
            while not self.stopping.is_set():
                batch = Batch()
                for msg in self.consumer.consume(num_messages=MAX_BATCH, timeout=BATCH_WAIT_S):
                    if msg.error():
                        log.error("kafka consume failed", extra=fields(error=str(msg.error())))
                        continue
                    batch.add(self.detector, msg.key(), msg.value())
                if batch.consumed == 0:
                    continue
                if not self._publish(batch.records):
                    # Stopping: the batch is consumed again after the restart.
                    return
                self._commit()
                self._count(batch)
        finally:
            self.consumer.close()
            self.producer.flush(5)

    def _publish(self, records: list) -> bool:
        """Writes the records, retrying the failed ones until they are written.

        Every message already passed the contract, so a failure here comes from
        Kafka, not from the data (ADR 0004). Returns False only when stopping.
        """
        wait = 1.0
        pending = records
        while pending:
            failed = []
            for r in pending:
                self.producer.produce(r.topic, value=r.value, key=r.key, headers=r.headers,
                                      on_delivery=_on_delivery(r, failed))
            self.producer.flush()
            if not failed:
                return True
            failures.labels("produce").inc()
            log.error("kafka produce failed, retrying",
                      extra=fields(error=failed[0][1], failed=len(failed), retry_in_s=wait))
            if self.stopping.wait(wait):
                return False
            wait = min(2 * wait, MAX_RETRY_WAIT_S)
            pending = [r for r, _ in failed]
        return True

    def _commit(self) -> None:
        # A failed commit only means these records are consumed again later, which
        # the seq check of the detector makes harmless, so it is not retried.
        try:
            self.consumer.commit(asynchronous=False)
        except KafkaException as e:
            failures.labels("commit").inc()
            log.warning("kafka commit failed, records will be consumed again", extra=fields(error=str(e)))

    def _count(self, batch: Batch) -> None:
        consumed.inc(batch.consumed)
        duplicates.inc(batch.duplicates)
        now = datetime.now(timezone.utc)
        for r in batch.records:
            if r.topic == TOPIC_DLQ:
                dead_lettered.inc()
                log.warning("message sent to DLQ", extra=fields(key=_text(r.key), error=_text(dict(r.headers)["error"])))
            else:
                alerts_published.labels(r.alert_type).inc()
                alert_delay.observe((now - r.ts).total_seconds())
                log.info("alert published", extra=fields(**json.loads(r.value)))

    def health(self) -> str | None:
        """Returns why the service is unhealthy, or None."""
        try:
            self.producer.list_topics(TOPIC_ALERTS, timeout=2)
        except KafkaException as e:
            return f"kafka: {e}"
        return None


def _on_delivery(record: OutRecord, failed: list):
    def callback(err, _msg):
        if err is not None:
            failed.append((record, str(err)))
    return callback


def _text(b: bytes | None) -> str:
    return b.decode(errors="replace") if b is not None else ""


def serve_http(addr: str, service: Service) -> ThreadingHTTPServer:
    """Serves GET /healthz and GET /metrics on addr, such as ":8083", in a background thread."""
    host, _, port = addr.rpartition(":")

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            path = self.path.split("?", 1)[0]
            if path == "/healthz":
                problem = service.health()
                self._reply(503 if problem else 200, "text/plain; charset=utf-8", (problem or "ok") + "\n")
            elif path == "/metrics":
                self._reply(200, CONTENT_TYPE_LATEST, generate_latest())
            else:
                self._reply(404, "text/plain; charset=utf-8", "404 page not found\n")

        def _reply(self, status, content_type, body):
            body = body.encode() if isinstance(body, str) else body
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass  # Prometheus scrapes every 15 seconds; request logs would be noise.

    server = ThreadingHTTPServer((host, int(port)), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def fields(**kwargs) -> dict:
    """Extra fields of a log line: log.info("msg", extra=fields(key=value))."""
    return {"fields": kwargs}


class JSONFormatter(logging.Formatter):
    """One JSON object per line, with the same keys as the slog output of the Go services."""

    def format(self, record: logging.LogRecord) -> str:
        level = {"WARNING": "WARN", "CRITICAL": "ERROR"}.get(record.levelname, record.levelname)
        entry = {
            "time": datetime.fromtimestamp(record.created, timezone.utc).isoformat(),
            "level": level,
            "msg": record.getMessage(),
            **getattr(record, "fields", {}),
        }
        if record.exc_info:
            entry["error"] = self.formatException(record.exc_info)
        return json.dumps(entry, default=str)
