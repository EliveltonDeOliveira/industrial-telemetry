-- Read only role for Grafana, which runs the SQL of its panels (ADR 0015).
-- The password comes from the psql variable grafana_password, which migrate.sh
-- sets from POSTGRES_GRAFANA_PASSWORD. Changing it later needs ALTER ROLE.
CREATE ROLE grafana LOGIN PASSWORD :'grafana_password';
GRANT USAGE ON SCHEMA public TO grafana;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO grafana;
-- Tables created by later migrations, which run as the same owner, are readable too.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO grafana;
-- A heavy panel cannot hold the database, and a write fails even if a grant slips.
ALTER ROLE grafana SET statement_timeout = '10s';
ALTER ROLE grafana SET default_transaction_read_only = on;
