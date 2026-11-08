"""Anomaly rules, without Kafka or any other I/O (ADR 0012).

- temperatura_alta and vibracao_alta: the reading is above mean + 3 standard
  deviations of the previous 60 running readings of that sensor, for 3 readings
  in a row. Only running readings count, because stopping and starting change
  both sensors without any fault.
- parada_longa: the machine has been stopped for more than 300 seconds.
- Cooldown: the same machine and type do not alert again within 600 seconds.

Time is the ts of the readings, not the clock of this service, so replaying the
same readings gives the same alerts.
"""

import statistics
import uuid
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime
from typing import Mapping

from .contract import SCHEMA_VERSION, Alert, Telemetry, validate_alert

# Fixed per type: the z-score does not grade urgency, since a slow rise always
# alerts just above the threshold and a jump alerts far above it.
SEVERITY = {"temperatura_alta": "alta", "vibracao_alta": "media", "parada_longa": "media"}

# Sensor rules, as alert type and reading field.
SENSORS = (("temperatura_alta", "temperature_c"), ("vibracao_alta", "vibration_rms"))

# alert_id derives from the reading that raised the alert, so a reading consumed
# again raises the same alert_id and the maintenance service ignores the repeat.
ALERT_NAMESPACE = uuid.uuid5(uuid.NAMESPACE_URL, "github.com/EliveltonDeOliveira/industrial-telemetry/alertas")

ENV_VARS = {
    "window": "ANOMALY_WINDOW",
    "z_threshold": "ANOMALY_Z_THRESHOLD",
    "consecutive": "ANOMALY_CONSECUTIVE",
    "stop_s": "ANOMALY_STOP_S",
    "cooldown_s": "ANOMALY_COOLDOWN_S",
}


@dataclass(frozen=True)
class Config:
    window: int = 60  # running readings of each sensor in the moving window
    z_threshold: float = 3.0  # standard deviations above the mean, upward only
    consecutive: int = 3  # readings in a row above the threshold before alerting
    stop_s: float = 300.0  # stopped for longer than this raises parada_longa
    cooldown_s: float = 600.0  # the same machine and type do not alert again before this

    def __post_init__(self):
        # Written as "not ... >" so NaN is rejected too.
        if not self.window >= 2:
            raise ValueError(f"{ENV_VARS['window']}: must be at least 2, got {self.window}")
        if not self.z_threshold > 0:
            raise ValueError(f"{ENV_VARS['z_threshold']}: must be positive, got {self.z_threshold}")
        if not self.consecutive >= 1:
            raise ValueError(f"{ENV_VARS['consecutive']}: must be at least 1, got {self.consecutive}")
        if not self.stop_s > 0:
            raise ValueError(f"{ENV_VARS['stop_s']}: must be positive, got {self.stop_s}")
        if not self.cooldown_s >= 0:
            raise ValueError(f"{ENV_VARS['cooldown_s']}: must not be negative, got {self.cooldown_s}")

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> "Config":
        values = {}
        for name, var in ENV_VARS.items():
            raw = env.get(var)
            if raw:
                convert = type(getattr(cls, name))
                try:
                    values[name] = convert(raw)
                except ValueError:
                    raise ValueError(f"{var}: {raw!r} is not a valid {convert.__name__}") from None
        return cls(**values)


@dataclass
class _Sensor:
    window: deque
    streak: int = 0  # running readings in a row above the threshold


@dataclass
class _Machine:
    sensors: dict
    boot_id: int = -1
    seq: int = -1
    stopped_since: datetime | None = None
    last_alert: dict = field(default_factory=dict)  # alert type -> ts of the last alert


class Detector:
    """Keeps the state of each machine in memory. It is rebuilt after a restart:
    the windows fill again in 60 running readings and the cooldowns start over."""

    def __init__(self, config: Config = Config()):
        self.config = config
        self._machines: dict[str, _Machine] = {}

    def process(self, t: Telemetry) -> list[Alert] | None:
        """Returns the alerts raised by t, or None when t repeats a reading already seen."""
        m = self._machines.get(t.machine_id)
        if m is None:
            m = _Machine(sensors={kind: _Sensor(deque(maxlen=self.config.window)) for kind, _ in SENSORS})
            self._machines[t.machine_id] = m

        # Same rule as the telemetry service (ADR 0003): seq only grows within a boot_id.
        if t.boot_id == m.boot_id and t.seq <= m.seq:
            return None
        m.boot_id, m.seq = t.boot_id, t.seq

        if t.state == "running":
            m.stopped_since = None
            candidates = list(self._sensor_rules(m, t))
        else:
            for s in m.sensors.values():
                s.streak = 0
            if m.stopped_since is None:
                m.stopped_since = t.ts
            stopped_s = (t.ts - m.stopped_since).total_seconds()
            candidates = []
            if stopped_s > self.config.stop_s:
                candidates.append(("parada_longa", round(stopped_s, 1), self.config.stop_s))

        alerts = []
        for alert_type, value, threshold in candidates:
            last = m.last_alert.get(alert_type)
            if last is not None and (t.ts - last).total_seconds() < self.config.cooldown_s:
                continue
            m.last_alert[alert_type] = t.ts
            alerts.append(self._alert(t, alert_type, value, threshold))
        return alerts

    def _sensor_rules(self, m: _Machine, t: Telemetry):
        """Yields (type, value, threshold) for each sensor above its threshold long enough."""
        for alert_type, reading_field in SENSORS:
            s = m.sensors[alert_type]
            value = getattr(t, reading_field)
            limit = None
            if len(s.window) == self.config.window:
                mean = statistics.fmean(s.window)
                sd = statistics.pstdev(s.window, mu=mean)
                # A flat window has no spread, and any change would be an infinite z.
                if sd > 0:
                    limit = mean + self.config.z_threshold * sd
            above = limit is not None and value > limit
            s.streak = s.streak + 1 if above else 0
            s.window.append(value)
            if s.streak >= self.config.consecutive:
                yield alert_type, value, round(limit, 3)

    def _alert(self, t: Telemetry, alert_type: str, value: float, threshold: float) -> Alert:
        alert = Alert(
            schema_version=SCHEMA_VERSION,
            alert_id=str(uuid.uuid5(ALERT_NAMESPACE, f"{t.machine_id}/{t.boot_id}/{t.seq}/{alert_type}")),
            machine_id=t.machine_id,
            ts=t.ts,
            type=alert_type,
            severity=SEVERITY[alert_type],
            value=float(value),
            threshold=float(threshold),
        )
        validate_alert(alert)
        return alert
