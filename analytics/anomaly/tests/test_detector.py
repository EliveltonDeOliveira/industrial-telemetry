import statistics
from dataclasses import replace
from datetime import datetime, timedelta, timezone

import pytest

from anomaly.contract import Telemetry, parse_alert
from anomaly.detector import Config, Detector

T0 = datetime(2026, 11, 7, 14, 0, tzinfo=timezone.utc)

# The default rules: window of 60, z above 3, 3 in a row, 300 s stopped, 600 s cooldown.
CONFIG = Config()

# Fills the window alternating around 50 C and 0.40 g: standard deviation 1 C and 0.01 g,
# so the temperature limit is 50 + 3 x 1 = 53.
BASE_TEMPS = [49.0, 51.0] * 30
BASE_VIBS = [0.39, 0.41] * 30


class Machine:
    """Feeds readings of one machine, one per second, to a detector."""

    def __init__(self, detector: Detector, machine_id: str = "maq-01", boot_id: int = 7):
        self.detector = detector
        self.machine_id = machine_id
        self.boot_id = boot_id
        self.seq = 0

    def reading(self, state="running", temp=50.0, vib=0.40, at=None):
        t = Telemetry(
            schema_version=1, machine_id=self.machine_id, boot_id=self.boot_id, seq=self.seq,
            ts=at or T0 + timedelta(seconds=self.seq), state=state, good_total=0, scrap_total=0,
            temperature_c=temp, vibration_rms=vib)
        self.seq += 1
        return t

    def send(self, **kwargs):
        return self.detector.process(self.reading(**kwargs))

    def warm_up(self):
        for temp, vib in zip(BASE_TEMPS, BASE_VIBS):
            assert self.send(temp=temp, vib=vib) == []

    def types(self, **kwargs):
        return [a.type for a in self.send(**kwargs)]


def new_machine(**kwargs):
    return Machine(Detector(CONFIG), **kwargs)


@pytest.mark.parametrize("name, temps, want", [
    ("third reading in a row above alerts", [60, 60, 60], [[], [], ["temperatura_alta"]]),
    ("two in a row do not alert", [60, 60, 50, 60, 60], [[], [], [], [], []]),
    ("downward does not alert", [40, 40, 40, 40], [[], [], [], []]),
    ("at the limit does not alert", [53, 53, 53], [[], [], []]),
])
def test_temperature_rule(name, temps, want):
    m = new_machine()
    m.warm_up()
    got = [m.types(temp=t) for t in temps]
    assert got == want


def test_alert_fields():
    m = new_machine()
    m.warm_up()
    m.send(temp=60)
    m.send(temp=60)
    [alert] = m.send(temp=70)
    # The threshold is mean + 3 standard deviations of the 60 readings before the alert.
    window = BASE_TEMPS[2:] + [60, 60]
    assert alert.type == "temperatura_alta"
    assert alert.severity == "alta"
    assert alert.value == 70.0
    assert alert.threshold == round(statistics.fmean(window) + 3 * statistics.pstdev(window), 3)
    assert alert.ts == T0 + timedelta(seconds=62)
    assert alert.machine_id == "maq-01"
    assert parse_alert(alert.to_json()) == alert


def test_vibration_rule():
    m = new_machine()
    m.warm_up()
    # Four times the normal vibration, as the vibracao_alta fault of the simulator.
    assert [m.types(vib=1.6) for _ in range(3)] == [[], [], ["vibracao_alta"]]


def test_no_alert_before_the_window_fills():
    m = new_machine()
    assert [m.types(temp=t) for t in (50, 50, 50, 99, 99)] == [[], [], [], [], []]


def test_flat_window():
    m = new_machine()
    for _ in range(60):
        m.send(temp=24.0)  # the micro:bit reports whole degrees
    # The first 25 meets a window with no spread and is skipped. Once it enters
    # the window, the next ones are above the limit: one degree up alerts.
    assert [m.types(temp=25.0) for _ in range(4)] == [[], [], [], ["temperatura_alta"]]


def test_stopped_readings_are_ignored_by_the_sensor_rules():
    m = new_machine()
    m.warm_up()
    # Stopping drops vibration to almost zero, which would make the restart look like a jump.
    for _ in range(10):
        assert m.send(state="stopped", temp=30.0, vib=0.02) == []
    assert all(m.send(temp=t, vib=v) == [] for t, v in zip(BASE_TEMPS, BASE_VIBS))


def test_a_stop_breaks_the_streak():
    m = new_machine()
    m.warm_up()
    m.send(temp=60)
    m.send(temp=60)
    m.send(state="stopped")
    assert m.types(temp=60) == []


@pytest.mark.parametrize("stopped_s, want", [
    (299, []),
    (300, []),
    (301, ["parada_longa"]),
])
def test_long_stop(stopped_s, want):
    m = new_machine()
    m.send(state="stopped", at=T0)
    assert m.types(state="stopped", at=T0 + timedelta(seconds=stopped_s)) == want


def test_long_stop_fields_and_repeat():
    m = new_machine()
    m.send(state="stopped", at=T0)
    [first] = m.send(state="stopped", at=T0 + timedelta(seconds=301.25))
    assert (first.type, first.severity, first.value, first.threshold) == ("parada_longa", "media", 301.2, 300.0)
    # Still stopped: the cooldown holds the next alert for 600 seconds.
    assert m.send(state="stopped", at=T0 + timedelta(seconds=900)) == []
    [again] = m.send(state="stopped", at=T0 + timedelta(seconds=901.25))
    assert again.value == 901.2


def test_running_resets_the_stop():
    m = new_machine()
    m.send(state="stopped", at=T0)
    m.send(state="running", at=T0 + timedelta(seconds=200))
    m.send(state="stopped", at=T0 + timedelta(seconds=250))
    assert m.send(state="stopped", at=T0 + timedelta(seconds=400)) == []
    assert m.types(state="stopped", at=T0 + timedelta(seconds=551)) == ["parada_longa"]


def test_cooldown_is_per_machine_and_type():
    detector = Detector(CONFIG)
    a, b = Machine(detector, "maq-01"), Machine(detector, "maq-02")
    a.warm_up()
    b.warm_up()
    for _ in range(2):
        a.send(temp=60)
        b.send(temp=60)
    assert a.types(temp=60) == ["temperatura_alta"]
    assert b.types(temp=60) == ["temperatura_alta"]
    # Still hot on maq-01: no new temperature alert, but vibration has its own cooldown.
    for _ in range(2):
        assert a.types(temp=99, vib=1.6) == []
    assert a.types(temp=99, vib=1.6) == ["vibracao_alta"]


def test_repeated_readings_are_dropped():
    m = new_machine()
    m.warm_up()
    reading = m.reading(temp=60)
    assert m.detector.process(reading) == []
    # Delivered again by Kafka: dropped, and it does not count towards the streak.
    assert m.detector.process(reading) is None
    assert m.detector.process(replace(reading, seq=0)) is None
    assert m.types(temp=60) == []


def test_new_boot_is_not_a_repeat():
    m = new_machine()
    m.warm_up()
    m.boot_id, m.seq = 8, 0  # the machine restarted: seq starts over
    assert m.send(temp=50.0) == []


def test_alert_id_is_derived_from_the_reading():
    def alerts():
        m = new_machine()
        m.warm_up()
        return [a for _ in range(3) for a in m.send(temp=60)]

    first, again = alerts(), alerts()
    assert [a.alert_id for a in first] == [a.alert_id for a in again]
    other = new_machine(machine_id="maq-02")
    other.warm_up()
    [alert] = [a for _ in range(3) for a in other.send(temp=60)]
    assert alert.alert_id != first[0].alert_id


@pytest.mark.parametrize("env, want", [
    ({}, Config()),
    ({"ANOMALY_WINDOW": "120", "ANOMALY_Z_THRESHOLD": "3.5", "ANOMALY_COOLDOWN_S": "60"},
     Config(window=120, z_threshold=3.5, cooldown_s=60.0)),
    ({"ANOMALY_WINDOW": ""}, Config()),
])
def test_config_from_env(env, want):
    assert Config.from_env(env) == want


@pytest.mark.parametrize("env, var", [
    ({"ANOMALY_WINDOW": "1"}, "ANOMALY_WINDOW"),
    ({"ANOMALY_WINDOW": "6.5"}, "ANOMALY_WINDOW"),
    ({"ANOMALY_Z_THRESHOLD": "nan"}, "ANOMALY_Z_THRESHOLD"),
    ({"ANOMALY_CONSECUTIVE": "0"}, "ANOMALY_CONSECUTIVE"),
    ({"ANOMALY_STOP_S": "abc"}, "ANOMALY_STOP_S"),
    ({"ANOMALY_COOLDOWN_S": "-1"}, "ANOMALY_COOLDOWN_S"),
])
def test_config_rejects(env, var):
    with pytest.raises(ValueError, match=f"^{var}: "):
        Config.from_env(env)
