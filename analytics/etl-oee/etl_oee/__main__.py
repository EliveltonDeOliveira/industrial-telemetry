"""ETL that writes the OEE of each machine and shift to ClickHouse oee_turno (ADR 0016).

It runs when it starts and then at 5 minutes past every hour. Each run
recomputes the shifts that ended in the lookback, from the readings in
telemetry_raw and the ideal cycle in the machines registry.

Configuration:

    CLICKHOUSE_DSN  ClickHouse HTTP address (http://industrial:industrial@clickhouse:8123/industrial)
    POSTGRES_DSN    PostgreSQL address (postgres://industrial:industrial@postgres:5432/industrial?sslmode=disable)
    ETL_TIMEZONE    time zone of the shifts (America/Sao_Paulo)
    ETL_SHIFTS      shifts in local hours (06-14,14-22,22-06)
    ETL_LOOKBACK_H  hours back to look for shifts that ended (24)

With --once it runs a single time and exits with 1 if the run fails, for a
backfill such as: ETL_LOOKBACK_H=72 python -m etl_oee --once
"""

import logging
import os
import signal
import sys
import threading
import time
from datetime import datetime, timezone

from .job import Config, next_run, run
from .logs import JSONFormatter, fields
from .store import DBStore

log = logging.getLogger("etl-oee")


def run_logged(cfg: Config) -> bool:
    started = time.monotonic()
    try:
        store = DBStore(cfg.clickhouse_dsn, cfg.postgres_dsn)
        try:
            rows = run(store, cfg, datetime.now(timezone.utc))
        finally:
            store.close()
    except Exception as e:
        # A failed run is not retried: the next one, an hour later, covers the same shifts.
        log.error("run failed", extra=fields(error=f"{type(e).__name__}: {e}"))
        return False
    log.info("oee written", extra=fields(
        rows=len(rows), shifts=sorted({f"{r[1]} {r[2]}" for r in rows}),
        duration_s=round(time.monotonic() - started, 3)))
    return True


def main() -> int:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JSONFormatter())
    logging.basicConfig(level=logging.INFO, handlers=[handler])
    try:
        cfg = Config.from_env(os.environ)
    except ValueError as e:
        log.error("invalid configuration", extra=fields(error=str(e)))
        return 1

    if "--once" in sys.argv[1:]:
        return 0 if run_logged(cfg) else 1

    stopping = threading.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda *_: stopping.set())
    log.info("etl-oee started", extra=fields(
        timezone=str(cfg.tz), shifts=[d.label for d in cfg.shifts], lookback_h=cfg.lookback.total_seconds() / 3600))
    while not stopping.is_set():
        run_logged(cfg)
        now = datetime.now(timezone.utc)
        stopping.wait((next_run(now) - now).total_seconds())
    return 0


if __name__ == "__main__":
    sys.exit(main())
