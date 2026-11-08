-- The time and pieces behind the factors of each shift, so the shifts API
-- answers like GET /machines/{id}/oee and a factor can be checked by hand (ADR 0016).
-- A single ALTER, because ClickHouse has no transactional DDL.
ALTER TABLE oee_turno
    ADD COLUMN shift_end DateTime('UTC') AFTER shift_start,
    ADD COLUMN planned_s Float64 AFTER shift_end,
    ADD COLUMN running_s Float64 AFTER planned_s,
    ADD COLUMN good      UInt64 AFTER running_s,
    ADD COLUMN scrap     UInt64 AFTER good;
