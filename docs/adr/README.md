# Decisões de arquitetura

Cada ADR registra uma decisão, o contexto em que foi tomada, as alternativas consideradas e as consequências. Uma decisão nova vira um ADR novo. Um ADR aceito não é reescrito: se a decisão mudar, um ADR novo o substitui e o antigo passa a apontar para ele.

| ADR | Decisão |
| --- | --- |
| [0001](0001-mqtt-na-borda-kafka-no-servidor.md) | MQTT na borda e Kafka no servidor |
| [0002](0002-clickhouse-e-postgresql.md) | ClickHouse para telemetria e PostgreSQL para manutenção |
| [0003](0003-idempotencia-por-boot-id-e-seq.md) | Idempotência por `boot_id` e `seq` |
| [0004](0004-dead-letter-queue.md) | Dead letter queue |
| [0005](0005-grafana-sem-frontend-proprio.md) | Grafana no lugar de frontend próprio |
| [0006](0006-validacao-do-contrato.md) | Validação do contrato sem biblioteca de JSON Schema |
| [0007](0007-migrations-com-script-proprio.md) | Migrations com script próprio e CLIs oficiais |
| [0008](0008-headers-da-dlq.md) | Headers da DLQ: motivo, origem e serviço |
| [0009](0009-regras-do-oee.md) | Regras de cálculo do OEE |
| [0010](0010-oee-no-grafana-pela-api.md) | OEE no Grafana pela API do telemetry |
