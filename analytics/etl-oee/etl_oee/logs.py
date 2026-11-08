"""JSON logs with the same keys as the slog output of the Go services."""

import json
import logging
from datetime import datetime, timezone


def fields(**kwargs) -> dict:
    """Extra fields of a log line: log.info("msg", extra=fields(key=value))."""
    return {"fields": kwargs}


class JSONFormatter(logging.Formatter):
    """One JSON object per line. The same as in analytics/anomaly, since each
    Python service is built on its own."""

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
