#!/bin/sh
# Applies each .sql file in /migrations/<engine>/ once, in file name order,
# and records it in the schema_migrations table of that database.
# Usage: migrate.sh postgres|clickhouse
set -eu

engine=$1

case $engine in
postgres)
	# psql reads PGHOST, PGUSER, PGPASSWORD and PGDATABASE from the environment.
	export PGOPTIONS="-c client_min_messages=warning"
	query() { psql -v ON_ERROR_STOP=1 -qtA -c "$1"; }
	# The file and its record run in one transaction, so a failed migration leaves no trace.
	# Secrets reach the migrations as psql variables, such as :'grafana_password'.
	apply() {
		psql -v ON_ERROR_STOP=1 -v grafana_password="$POSTGRES_GRAFANA_PASSWORD" -q --single-transaction \
			-f "$1" -c "INSERT INTO schema_migrations (version) VALUES ('$2')"
	}
	query "CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"
	;;
clickhouse)
	# stdin is closed because clickhouse-client waits for INSERT data on a non-terminal stdin.
	ch() { clickhouse-client --host "$CLICKHOUSE_HOST" --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --database "$CLICKHOUSE_DB" "$@" </dev/null; }
	query() { ch --query "$1"; }
	# ClickHouse has no transactional DDL: a failed migration must be fixed by hand.
	apply() { ch --queries-file "$1" && query "INSERT INTO schema_migrations (version) VALUES ('$2')"; }
	query "CREATE TABLE IF NOT EXISTS schema_migrations (version String, applied_at DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY version"
	;;
*)
	echo "usage: migrate.sh postgres|clickhouse" >&2
	exit 2
	;;
esac

for file in /migrations/"$engine"/*.sql; do
	version=$(basename "$file" .sql)
	if [ "$(query "SELECT count(*) FROM schema_migrations WHERE version = '$version'")" = 0 ]; then
		echo "$engine: applying $version"
		apply "$file" "$version"
	fi
done
echo "$engine: up to date"
