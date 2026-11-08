#!/usr/bin/env python3
"""Prints the numbers of a load test window, read from Prometheus (ADR 0019).

Usage, from deploy/, after the window ends:
    loadtest/report.py --minutes 30
    loadtest/report.py --minutes 5 --end 2026-11-08T21:50:00Z

Only the standard library, so it runs with any Python 3 on the host.
"""

import argparse
import json
import urllib.parse
import urllib.request
from datetime import datetime, timezone

FAILURES = ('{__name__=~"simulator_publish_failures_total|bridge_produce_failures_total|telemetry_failures_total'
            '|anomaly_failures_total|maintenance_failures_total"}')


def query(url: str, expr: str, at: float) -> list[tuple[dict, float]]:
    params = urllib.parse.urlencode({"query": expr, "time": at})
    with urllib.request.urlopen(f"{url}/api/v1/query?{params}", timeout=30) as resp:
        body = json.load(resp)
    return [(r["metric"], float(r["value"][1])) for r in body["data"]["result"]]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--minutes", type=int, required=True, help="length of the window, ending at --end")
    parser.add_argument("--end", help="end of the window, RFC 3339 (now)")
    parser.add_argument("--prometheus", default="http://localhost:9090")
    args = parser.parse_args()

    end = datetime.fromisoformat(args.end).timestamp() if args.end else datetime.now(timezone.utc).timestamp()
    w = f"{args.minutes * 60}s"
    print(f"window: {args.minutes} min ending {datetime.fromtimestamp(end, timezone.utc):%Y-%m-%d %H:%M:%S} UTC")

    def show(title: str, expr: str, unit: str = "", label: str | None = None, fmt: str = "{:.2f}") -> None:
        rows = query(args.prometheus, expr, end)
        if not rows:
            print(f"  {title}: no data")
        for metric, value in sorted(rows, key=lambda r: r[0].get(label or "", "")):
            name = f"{title} {metric[label]}" if label else title
            print(f"  {name}: {fmt.format(value)}{unit}")

    print("throughput, readings per second")
    show("simulators published", f"sum(rate(simulator_messages_published_total[{w}]))", "/s", fmt="{:.1f}")
    show("bridge received", f"sum(rate(bridge_messages_received_total[{w}]))", "/s", fmt="{:.1f}")
    show("telemetria.bruta written", f'sum(rate(kafka_partition_end_offset{{topic="telemetria.bruta"}}[{w}]))', "/s",
         fmt="{:.1f}")
    show("telemetry inserted", f"sum(rate(telemetry_rows_inserted_total[{w}]))", "/s", fmt="{:.1f}")
    show("anomaly consumed", f"sum(rate(anomaly_records_consumed_total[{w}]))", "/s", fmt="{:.1f}")

    print("latency from the reading ts")
    for q in (0.5, 0.95, 0.99):
        show(f"ingest p{round(q * 100)}",
             f"histogram_quantile({q}, sum by (le) (increase(telemetry_ingest_delay_seconds_bucket[{w}])))", " s")
    show("alert p95", f"histogram_quantile(0.95, sum by (le) (increase(anomaly_alert_delay_seconds_bucket[{w}])))", " s")
    show("work order p95",
         f"histogram_quantile(0.95, sum by (le) (increase(maintenance_alert_delay_seconds_bucket[{w}])))", " s")
    show("alerts published", f"sum(increase(anomaly_alerts_published_total[{w}]))", fmt="{:.0f}")
    show("work orders opened", f"sum(increase(maintenance_work_orders_opened_total[{w}]))", fmt="{:.0f}")

    print("consumer lag, messages")
    show("max", f"max_over_time(sum by (group) (kafka_consumergroup_lag)[{w}:15s])", label="group", fmt="{:.0f}")
    show("mean", f"avg_over_time(sum by (group) (kafka_consumergroup_lag)[{w}:15s])", label="group", fmt="{:.0f}")
    show("at the end", "sum by (group) (kafka_consumergroup_lag)", label="group", fmt="{:.0f}")

    print("errors")
    show("failed attempts", f"round(sum by (job) (increase({FAILURES}[{w}])))", label="job", fmt="{:.0f}")
    show("messages to the DLQ", f'round(sum(increase(kafka_partition_end_offset{{topic="telemetria.dlq"}}[{w}])))',
         fmt="{:.0f}")

    print("services, from their own process metrics")
    show("CPU", f"rate(process_cpu_seconds_total[{w}])", " cores", label="job", fmt="{:.3f}")
    show("max memory", f"max_over_time(process_resident_memory_bytes[{w}]) / 2^20", " MiB", label="job", fmt="{:.0f}")


if __name__ == "__main__":
    main()
