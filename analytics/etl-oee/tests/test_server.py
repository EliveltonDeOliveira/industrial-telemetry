import urllib.error
import urllib.request

from prometheus_client import REGISTRY

from etl_oee.server import Health, serve_http


def sample(name, **labels):
    return REGISTRY.get_sample_value(name, labels) or 0.0


def get(server, path):
    url = f"http://127.0.0.1:{server.server_address[1]}{path}"
    try:
        with urllib.request.urlopen(url) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def test_health_and_metrics():
    health = Health()
    server = serve_http("127.0.0.1:0", health)
    try:
        assert get(server, "/healthz") == (503, "no run yet\n")

        before = {k: sample("etl_oee_runs_total", result=k) for k in ("success", "failure")}
        rows_before = sample("etl_oee_rows_written_total")
        health.record(12, 0.3, 1794170000.0, None)
        assert get(server, "/healthz") == (200, "ok\n")
        assert sample("etl_oee_runs_total", result="success") == before["success"] + 1
        assert sample("etl_oee_rows_written_total") == rows_before + 12
        assert sample("etl_oee_last_success_timestamp_seconds") == 1794170000.0

        health.record(0, 0.1, 1794173600.0, "OperationalError: connection refused")
        assert get(server, "/healthz") == (503, "last run failed: OperationalError: connection refused\n")
        assert sample("etl_oee_runs_total", result="failure") == before["failure"] + 1
        # A failure keeps the time of the last success.
        assert sample("etl_oee_last_success_timestamp_seconds") == 1794170000.0

        status, body = get(server, "/metrics")
        assert status == 200 and "etl_oee_run_duration_seconds_bucket" in body
        assert get(server, "/other")[0] == 404
    finally:
        server.shutdown()
