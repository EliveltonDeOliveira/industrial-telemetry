# industrial-telemetry

[![CI](https://github.com/EliveltonDeOliveira/industrial-telemetry/actions/workflows/ci.yml/badge.svg)](https://github.com/EliveltonDeOliveira/industrial-telemetry/actions/workflows/ci.yml)

Backend orientado a eventos para telemetria industrial. Máquinas, simuladas ou um micro:bit real, publicam leituras por MQTT, o Kafka distribui os eventos, o ClickHouse guarda a série temporal e o OEE é calculado em tempo real e exibido no Grafana.

> Em desenvolvimento. O monitoramento com simulador está pronto. Anomalias, ordens de manutenção e hardware real vêm nas próximas fases.

## Como rodar

Precisa só de Docker com o Compose.

```sh
cd deploy
docker compose up -d
```

Abra o Grafana em http://localhost:3000 (usuário `admin` e senha `admin`, credenciais de desenvolvimento). O dashboard "OEE por máquina" mostra as máquinas simuladas.

## Testes

```sh
go test ./...
```

Os testes de integração rodam com a stack de pé e as variáveis dos bancos definidas:

```sh
CLICKHOUSE_TEST_DSN='clickhouse://industrial:industrial@localhost:9000/industrial' \
POSTGRES_TEST_DSN='postgres://industrial:industrial@localhost:5432/industrial?sslmode=disable' \
go test ./...
```

As decisões de arquitetura ficam em [`docs/adr/`](docs/adr/).

## Licença

[MIT](LICENSE)
