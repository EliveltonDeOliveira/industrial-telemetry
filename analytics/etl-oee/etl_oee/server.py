"""Metrics and health of the ETL runs, served on GET /metrics and GET /healthz (ADR 0017)."""

import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from prometheus_client import CONTENT_TYPE_LATEST, Counter, Gauge, Histogram, generate_latest

runs = Counter("etl_oee_runs", "Runs of the ETL, by result.", ["result"])
rows_written = Counter("etl_oee_rows_written", "Rows written to oee_turno, one per machine and shift.")
run_duration = Histogram("etl_oee_run_duration_seconds", "Time of a run, from reading the machines to writing oee_turno.",
                         buckets=[0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300])
last_success = Gauge("etl_oee_last_success_timestamp_seconds",
                     "Unix time of the last run that succeeded. time() minus it is how long the ETL has been failing.")
for result in ("success", "failure"):
    runs.labels(result)  # both series exist from the start, at 0


class Health:
    """The result of the last run, for GET /healthz."""

    def __init__(self):
        self._lock = threading.Lock()
        self._problem = "no run yet"

    def record(self, rows: int, duration_s: float, finished_at: float, error: str | None) -> None:
        run_duration.observe(duration_s)
        if error is None:
            runs.labels("success").inc()
            rows_written.inc(rows)
            last_success.set(finished_at)
        else:
            runs.labels("failure").inc()
        with self._lock:
            self._problem = None if error is None else f"last run failed: {error}"

    def problem(self) -> str | None:
        with self._lock:
            return self._problem


def serve_http(addr: str, health: Health) -> ThreadingHTTPServer:
    """Serves GET /healthz and GET /metrics on addr, such as ":8086", in a background thread."""
    host, _, port = addr.rpartition(":")

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            path = self.path.split("?", 1)[0]
            if path == "/healthz":
                problem = health.problem()
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
