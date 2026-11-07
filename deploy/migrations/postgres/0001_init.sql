-- Mini CMMS: machine registry, alerts received from Kafka and the work orders they open.

CREATE TYPE alert_type AS ENUM ('temperatura_alta', 'vibracao_alta', 'parada_longa');
CREATE TYPE alert_severity AS ENUM ('baixa', 'media', 'alta');
CREATE TYPE work_order_status AS ENUM ('aberta', 'em_andamento', 'concluida', 'cancelada');

CREATE TABLE machines (
    machine_id    text PRIMARY KEY,
    name          text NOT NULL,
    ideal_cycle_s double precision NOT NULL CHECK (ideal_cycle_s > 0), -- seconds per part
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE work_orders (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    machine_id text NOT NULL REFERENCES machines,
    alert_type alert_type NOT NULL,
    status     work_order_status NOT NULL DEFAULT 'aberta',
    assignee   text,
    opened_at  timestamptz NOT NULL DEFAULT now(),
    closed_at  timestamptz,
    -- closed_at is filled exactly when the order reaches a final state.
    CHECK ((status IN ('concluida', 'cancelada')) = (closed_at IS NOT NULL))
);

-- At most one active order per machine and alert type.
CREATE UNIQUE INDEX work_orders_one_active_idx
    ON work_orders (machine_id, alert_type)
    WHERE status IN ('aberta', 'em_andamento');

CREATE TABLE work_order_events (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    work_order_id bigint NOT NULL REFERENCES work_orders,
    from_status   work_order_status, -- null when the order is created
    to_status     work_order_status NOT NULL,
    assignee      text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX work_order_events_work_order_id_idx ON work_order_events (work_order_id);

CREATE TABLE alerts (
    alert_id      uuid PRIMARY KEY, -- comes from the message, so a redelivered alert is not stored twice
    machine_id    text NOT NULL REFERENCES machines,
    ts            timestamptz NOT NULL,
    alert_type    alert_type NOT NULL,
    severity      alert_severity NOT NULL,
    value         double precision NOT NULL,
    threshold     double precision NOT NULL,
    work_order_id bigint NOT NULL REFERENCES work_orders, -- order opened by this alert, or the active one it joined
    received_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX alerts_work_order_id_idx ON alerts (work_order_id);
CREATE INDEX alerts_ts_idx ON alerts (ts);
