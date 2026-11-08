"""anomaly consumes telemetria.bruta with its own consumer group, applies the
anomaly rules of ADR 0012 and publishes the alerts to alertas. Messages that
break the contract go to telemetria.dlq. Offsets are committed only after the
alerts are written, so delivery is at least once.

Configuration:

    KAFKA_BROKERS        comma separated seed brokers (kafka:9092)
    HTTP_ADDR            address of the HTTP server with /healthz and /metrics (:8083)
    ANOMALY_WINDOW       running readings in the z-score window (60)
    ANOMALY_Z_THRESHOLD  standard deviations above the mean (3)
    ANOMALY_CONSECUTIVE  readings in a row above the threshold (3)
    ANOMALY_STOP_S       seconds stopped before parada_longa (300)
    ANOMALY_COOLDOWN_S   seconds before the same machine and type alert again (600)

Run with: python -m anomaly
"""

import logging
import os
import signal
import sys

from .detector import Config, Detector
from .service import SERVICE, JSONFormatter, Service, fields, log, serve_http


def main() -> int:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JSONFormatter())
    logging.basicConfig(level=logging.INFO, handlers=[handler])

    try:
        config = Config.from_env(os.environ)
    except ValueError as e:
        log.error("invalid configuration", extra=fields(error=str(e)))
        return 1
    brokers = os.environ.get("KAFKA_BROKERS") or "kafka:9092"
    http_addr = os.environ.get("HTTP_ADDR") or ":8083"

    service = Service(brokers, Detector(config))
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: service.stopping.set())
    server = serve_http(http_addr, service)

    log.info(f"{SERVICE} started", extra=fields(kafka_brokers=brokers, http_addr=http_addr, **vars(config)))
    service.run()
    server.shutdown()
    log.info(f"{SERVICE} stopped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
