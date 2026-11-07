-- Raw telemetry, one row per reading.
-- A redelivered message becomes an identical row with the same sorting key, and
-- ReplacingMergeTree collapses those rows during merges. Queries that need exact
-- results before a merge use FINAL.
CREATE TABLE telemetry_raw
(
    machine_id    LowCardinality(String),
    boot_id       UInt32,
    seq           UInt64,
    ts            DateTime64(3, 'UTC'),
    state         LowCardinality(String),
    good_total    UInt64,
    scrap_total   UInt64,
    temperature_c Float64,
    vibration_rms Float64
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (machine_id, ts, boot_id, seq);

-- OEE per machine and shift, written by etl-oee every hour.
-- Each run rewrites the shifts it covers, and ReplacingMergeTree keeps the row
-- with the latest computed_at.
CREATE TABLE oee_turno
(
    machine_id   LowCardinality(String),
    shift_date   Date,                   -- local date the shift starts on
    shift        LowCardinality(String), -- shift label, for example '06-14'
    shift_start  DateTime('UTC'),
    availability Float64,
    performance  Float64,
    quality      Float64,
    oee          Float64,
    computed_at  DateTime('UTC') DEFAULT now()
)
ENGINE = ReplacingMergeTree(computed_at)
ORDER BY (machine_id, shift_date, shift);
