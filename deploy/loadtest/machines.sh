#!/bin/sh
# Registers and removes the machines of the load test, carga-01, carga-02...,
# published by simulator-carga (ADR 0019). Registered, their alerts become work
# orders and etl-oee computes them, as for any machine.
#
# Usage, from deploy/:
#   loadtest/machines.sh add 4000   registers carga-01 to carga-4000
#   loadtest/machines.sh readings 500 32
#                                   writes synthetic readings of carga-01 to
#                                   carga-500, one per second over the 32 hours
#                                   before the current hour, to time etl-oee
#                                   with whole shifts
#   loadtest/machines.sh remove     deletes them with their alerts, work orders,
#                                   readings and OEE per shift
#
# Remove only after simulator-carga is back to 0 machines and the lag of
# maintenance is 0, or a late alert of a removed machine goes to the DLQ.
set -eu

psql_run() {
	docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
}

clickhouse_run() {
	docker compose exec -T clickhouse sh -c \
		'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --database "$CLICKHOUSE_DB" --multiquery'
}

number() {
	case $1 in
	'' | *[!0-9]*)
		echo "usage: $0 add <machines> | readings <machines> <hours> | remove" >&2
		exit 2
		;;
	esac
}

case ${1:-} in
add)
	count=${2:-}
	number "$count"
	# Machine N uses the simulator profile (N - 1) % 3, so ideal_cycle_s follows
	# maq-01 to maq-03. The ID has at least two digits, like the simulator.
	psql_run <<-SQL
		INSERT INTO machines (machine_id, name, ideal_cycle_s)
		SELECT 'carga-' || CASE WHEN n < 10 THEN '0' ELSE '' END || n, 'Teste de carga',
		       (ARRAY[4.0, 6.0, 2.5])[(n - 1) % 3 + 1]
		FROM generate_series(1, $count) AS n
		ON CONFLICT (machine_id) DO NOTHING;
		SELECT count(*) AS load_test_machines FROM machines WHERE machine_id LIKE 'carga-%';
	SQL
	;;
readings)
	count=${2:-}
	hours=${3:-}
	number "$count"
	number "$hours"
	# Straight into telemetry_raw, without the pipeline: what is measured is the
	# ETL reading whole shifts. Each machine runs with a 1 minute stop every 15
	# minutes and the cycle of its simulator profile.
	clickhouse_run <<-SQL
		INSERT INTO telemetry_raw
		SELECT
		    concat('carga-', if(m < 10, '0', ''), toString(m)) AS machine_id,
		    toUInt32(m) AS boot_id,
		    s AS seq,
		    toDateTime64(toStartOfHour(now('UTC')), 3, 'UTC') - toIntervalSecond($hours * 3600 - s) AS ts,
		    if(s % 900 < 60, 'stopped', 'running') AS state,
		    toUInt64(s / cycle * 0.97) AS good_total,
		    toUInt64(s / cycle * 0.03) AS scrap_total,
		    40 + (m % 3) * 10 + randNormal(0, 0.3) AS temperature_c,
		    abs(0.4 + randNormal(0, 0.02)) AS vibration_rms
		FROM (
		    SELECT intDiv(number, $hours * 3600) + 1 AS m, number % ($hours * 3600) AS s,
		           [4.5, 6.6, 2.8][(m - 1) % 3 + 1] AS cycle
		    FROM numbers($count * $hours * 3600)
		);
		SELECT count() AS synthetic_readings FROM telemetry_raw
		WHERE machine_id LIKE 'carga-%' AND ts < toStartOfHour(now('UTC'));
	SQL
	;;
remove)
	psql_run <<-'SQL'
		BEGIN;
		DELETE FROM work_order_events
		WHERE work_order_id IN (SELECT id FROM work_orders WHERE machine_id LIKE 'carga-%');
		DELETE FROM alerts WHERE machine_id LIKE 'carga-%';
		DELETE FROM work_orders WHERE machine_id LIKE 'carga-%';
		DELETE FROM machines WHERE machine_id LIKE 'carga-%';
		COMMIT;
	SQL
	clickhouse_run <<-'SQL'
		DELETE FROM telemetry_raw WHERE machine_id LIKE 'carga-%';
		DELETE FROM oee_turno WHERE machine_id LIKE 'carga-%';
	SQL
	echo "load test machines removed"
	;;
*)
	number ""
	;;
esac
