# industrial-telemetry

[![CI](https://github.com/EliveltonDeOliveira/industrial-telemetry/actions/workflows/ci.yml/badge.svg)](https://github.com/EliveltonDeOliveira/industrial-telemetry/actions/workflows/ci.yml)

Backend orientado a eventos para telemetria industrial:

- Máquinas publicam leituras por MQTT, e o Kafka distribui os eventos.
- O ClickHouse guarda a série temporal, e o OEE é calculado em tempo real.
- Anomalias viram ordens de manutenção num mini CMMS no PostgreSQL.

Tudo sobe com `docker compose up`, com máquinas simuladas e sem precisar de hardware.

## O que tem aqui

- **Ingestão confiável.**
  - A entrega é pelo menos uma vez em todo o caminho: a bridge só confirma a mensagem MQTT depois que o Kafka grava, e os consumidores só confirmam o offset depois de gravar no banco.
  - Repetidos são descartados por `boot_id` e `seq`, e a tabela do ClickHouse remove o que escapar.
- **OEE em tempo real e por turno.**
  - O cálculo é uma função pura em Go. O ETL horário tem a mesma função em Python, e as duas passam pelos mesmos casos de teste, em [`contracts/oee/cases.json`](contracts/oee/cases.json).
- **Anomalias viram ordens.**
  - Um detector por z-score e parada longa publica alertas, calibrado com 20 horas-máquina de leituras.
  - O `maintenance` transforma cada alerta em ordem numa transação, com uma ordem ativa por máquina e tipo.
  - Uma API REST valida as transições de estado da ordem.
- **Falhas com destino definido.**
  - Mensagem que quebra o contrato vai para uma DLQ com o motivo.
  - Banco ou broker fora do ar pausa o consumidor sem perder nada.
  - Recusa do banco tem 3 tentativas antes da DLQ.
- **Observabilidade.**
  - Cada serviço mede vazão, erros e a latência contada a partir do horário da leitura.
  - Um exportador próprio mede o lag dos consumer groups.
  - Um dashboard da plataforma mostra a saúde do pipeline inteiro.
- **Teste de carga com os gargalos achados e medidos.** Os resultados estão na seção [Números medidos](#números-medidos).

## Arquitetura

```mermaid
flowchart LR
    sim["simulator (Go)"] -- MQTT --> mosq[Mosquitto]
    gw["micro:bit + gateway no Pi 5 (Go)"] -. "MQTT, Fase 4" .-> mosq
    mosq --> bridge["bridge (Go)"]
    bridge --> raw[["Kafka: telemetria.bruta"]]
    raw --> tel["telemetry (Go)"]
    raw --> anom["anomaly (Python)"]
    anom --> alertas[["Kafka: alertas"]]
    alertas --> maint["maintenance (Go)"]
    tel --> ch[("ClickHouse")]
    maint --> pg[("PostgreSQL")]
    ch <--> etl["etl-oee (Python), a cada hora"]
    pg --> etl
    ch --> graf[Grafana]
    pg --> graf
    tel -- "API de OEE" --> graf
    prom[Prometheus] --> graf
    bridge & tel & anom & maint -.-> dlq[["Kafka: telemetria.dlq"]]
```

As setas tracejadas para a `telemetria.dlq` levam as mensagens que quebram o contrato ou que o banco recusa. A do gateway é a Fase 4, ainda por fazer. O Prometheus coleta `/metrics` de todos os serviços, e o `kafka-lag` lê do Kafka o lag dos consumer groups.

| Serviço | Linguagem | Consome | Produz ou grava | HTTP |
| --- | --- | --- | --- | --- |
| `simulator` | Go | nada | MQTT | `:8084`, `POST /faults` |
| `bridge` | Go | MQTT | `telemetria.bruta`, `telemetria.dlq` | `:8080` |
| `telemetry` | Go | `telemetria.bruta` | ClickHouse `telemetry_raw` | `:8081`, API de monitoramento e OEE |
| `anomaly` | Python | `telemetria.bruta` | `alertas` | `:8083` |
| `maintenance` | Go | `alertas` | PostgreSQL `alerts`, `work_orders` | `:8082`, API do mini CMMS |
| `etl-oee` | Python | ClickHouse e PostgreSQL | ClickHouse `oee_turno` | `:8086` |
| `kafka-lag` | Go | grupos e offsets do Kafka | métricas do Prometheus | `:8087` |

Todo serviço expõe `/healthz` e `/metrics`. O formato das mensagens está em [`contracts/`](contracts/), com JSON Schema e exemplos lidos pelos testes em Go e em Python.

### O caminho de uma leitura

Cada etapa só confirma depois que a seguinte guardou a leitura. Uma queda no meio faz a leitura chegar de novo, e a repetição é descartada.

```mermaid
sequenceDiagram
    participant M as máquina
    participant Q as Mosquitto
    participant B as bridge
    participant K as Kafka
    participant T as telemetry
    participant C as ClickHouse
    M->>Q: PUBLISH com QoS 1
    Q-->>M: PUBACK
    Q->>B: PUBLISH com QoS 1, sessão persistente
    B->>K: produce com chave machine_id
    K-->>B: gravado
    B-->>Q: PUBACK, só depois do Kafka
    T->>K: poll
    K-->>T: lote de leituras
    T->>T: descarta seq repetido do mesmo machine_id e boot_id
    T->>C: insert do lote, 1000 linhas ou 1 segundo
    C-->>T: ok
    T->>K: commit do offset, só depois do insert
```

- Se a bridge cair antes do PUBACK, o Mosquitto entrega a mensagem de novo.
- Se o `telemetry` cair antes do commit, o Kafka entrega o lote de novo.
- O descarte por `seq` e a tabela `ReplacingMergeTree` tornam a repetição inofensiva.
- Uma mensagem que quebra o contrato vai para a `telemetria.dlq`, com o motivo num header, e o pipeline segue.

## Números medidos

Medidos com o próprio compose, num notebook com 12 threads e 16 GB de RAM, cada máquina simulada mandando 1 leitura por segundo. O método e todos os degraus estão em [`docs/teste-de-carga.md`](docs/teste-de-carga.md).

| Cenário | Resultado |
| --- | --- |
| 500 máquinas por 30 minutos | 503 leituras por segundo em todas as etapas, ingestão p95 de 0,98 s, lag máximo de cerca de 1 s de leituras, nenhuma falha, 1 núcleo de CPU no total |
| Falha injetada com as 500 máquinas | 0,30 s da leitura ao alerta e 0,31 s até a ordem gravada |
| 6000 máquinas | 6003 leituras por segundo em todas as etapas, ingestão p95 de 0,24 s |
| Primeiro gargalo, corrigido | A janela de 20 mensagens em voo do Mosquitto limitava a bridge a 1584 por segundo. Com a janela em 1000, a bridge acompanhou 10.000 |
| Limite atual | O `anomaly`, em Python numa thread só, satura um núcleo perto de 8000 leituras por segundo. O lag dele cresce, sem perda, porque o Kafka guarda as leituras |
| ETL por turno | 500 máquinas, 3 turnos inteiros e 43,2 milhões de leituras em 63 s |

## Como rodar

Precisa só de Docker com o Compose.

```sh
cd deploy
docker compose up -d
docker compose ps -a   # kafka-init e os *-migrate terminam com código 0, o resto fica no ar
```

A primeira subida compila as imagens dos serviços e leva alguns minutos.

| Endereço | O que é |
| --- | --- |
| http://localhost:3000 | Grafana, com usuário `admin` e senha `admin` (credenciais de desenvolvimento). Dashboards "OEE por máquina", "Manutenção" e "Plataforma" |
| http://localhost:9090 | Prometheus |
| http://localhost:8081/machines | API do `telemetry` |
| http://localhost:8082/work-orders | API do `maintenance` |

As credenciais e portas podem ser trocadas num `deploy/.env`, a partir de [`deploy/.env.example`](deploy/.env.example). Fora o Mosquitto, que recebe as leituras do gateway, todas as portas ficam em `127.0.0.1`.

### Demonstração: de uma falha a uma ordem fechada

Espere cerca de 1 minuto depois da subida, para o `anomaly` encher a janela de 60 leituras de cada máquina. Então injete uma falha de vibração:

```sh
curl -X POST localhost:8084/faults -d '{"machine_id": "maq-01", "type": "vibracao_alta", "duration_s": 120}'
```

Em poucos segundos, o alerta aparece como marca vermelha no gráfico de vibração do "OEE por máquina", e a ordem abre sozinha:

```mermaid
sequenceDiagram
    actor U as você
    participant S as simulator
    participant A as anomaly
    participant K as Kafka, tópico alertas
    participant W as maintenance
    participant P as PostgreSQL
    participant G as Grafana
    U->>S: POST /faults, vibracao_alta
    S->>A: leituras com vibração 4 vezes maior, pelo MQTT, bridge e Kafka
    A->>A: 3 leituras seguidas acima da média mais 3 desvios padrão
    A->>K: alerta com alert_id determinístico
    K->>W: alerta
    W->>P: BEGIN e busca da ordem ativa com FOR UPDATE
    alt nenhuma ordem ativa para a máquina e o tipo
        W->>P: abre a ordem e grava o evento aberta
    end
    W->>P: grava o alerta com ON CONFLICT DO NOTHING
    W->>P: COMMIT
    W->>K: commit do offset
    G->>P: marca do alerta e ordens ativas
    U->>W: PATCH /work-orders/:id
```

O `alert_id` é um UUID versão 5 de `machine_id`, `boot_id`, `seq` e tipo do alerta, então reprocessar as mesmas leituras não duplica nem o alerta nem a ordem.

```sh
curl 'localhost:8082/work-orders?status=aberta&machine_id=maq-01'
curl -X PATCH localhost:8082/work-orders/<id> -d '{"status": "em_andamento", "assignee": "equipe-mecanica"}'
curl -X PATCH localhost:8082/work-orders/<id> -d '{"status": "concluida"}'
curl localhost:8082/work-orders/<id>   # histórico de estados e alertas ligados à ordem
```

Os estados de uma ordem:

```mermaid
stateDiagram-v2
    [*] --> aberta: alerta sem ordem ativa
    aberta --> em_andamento: PATCH
    em_andamento --> concluida: PATCH
    aberta --> cancelada: PATCH
    em_andamento --> cancelada: PATCH
    concluida --> [*]
    cancelada --> [*]
```

Qualquer outra transição responde 409, assim como pedir o estado atual de novo ou mudar uma ordem já fechada. Os outros tipos de falha são `temperatura_alta` e `parada_longa`. A parada longa só alerta depois de 5 minutos, então precisa de `duration_s` acima de 300.

### API

| Serviço | Rota | O que faz |
| --- | --- | --- |
| `telemetry` | `GET /machines` | máquinas e última leitura |
| `telemetry` | `GET /machines/{id}/oee?from=&to=` | OEE de um período, até 7 dias |
| `telemetry` | `GET /machines/{id}/oee/shifts?date=` | OEE por turno, calculado pelo ETL |
| `maintenance` | `GET /work-orders?status=&machine_id=` | lista as ordens |
| `maintenance` | `GET /work-orders/{id}` | detalhe, histórico e alertas |
| `maintenance` | `PATCH /work-orders/{id}` | muda estado ou responsável, validando a transição |
| `simulator` | `POST /faults` | injeta uma falha numa máquina |

## Decisões

Cada decisão tem um ADR em [`docs/adr/`](docs/adr/README.md), com o contexto, as alternativas e as consequências. Algumas das principais:

- [MQTT na borda e Kafka no servidor](docs/adr/0001-mqtt-na-borda-kafka-no-servidor.md), e [ClickHouse para a série temporal e PostgreSQL para o CMMS](docs/adr/0002-clickhouse-e-postgresql.md).
- [Idempotência por `boot_id` e `seq`](docs/adr/0003-idempotencia-por-boot-id-e-seq.md) e [o que vai para a DLQ](docs/adr/0004-dead-letter-queue.md).
- [Regras do OEE](docs/adr/0009-regras-do-oee.md), com contadores que reiniciam junto com a máquina, e o [ETL por turno](docs/adr/0016-etl-de-oee-por-turno.md), com fuso e horário de verão.
- [Regras de anomalia medidas com dados reais](docs/adr/0012-regras-de-anomalia.md): 1 alerta falso a cada 20 horas por máquina.
- [Alertas viram ordens](docs/adr/0013-alertas-viram-ordens.md): uma transação por alerta, com `FOR UPDATE`.
- [Métricas](docs/adr/0017-metricas-de-latencia-requisicoes-e-lag.md), [dashboard da plataforma](docs/adr/0018-dashboard-da-plataforma.md) e [teste de carga](docs/adr/0019-teste-de-carga.md).

## Testes e CI

```sh
go test ./...
```

Os testes de integração rodam com os bancos de pé e as variáveis definidas. Cada `run` sobe o banco, espera o healthcheck e aplica as migrations:

```sh
(cd deploy && docker compose run --rm postgres-migrate && docker compose run --rm clickhouse-migrate)
CLICKHOUSE_TEST_DSN='clickhouse://industrial:industrial@localhost:9000/industrial' \
POSTGRES_TEST_DSN='postgres://industrial:industrial@localhost:5432/industrial?sslmode=disable' \
go test -count=1 ./...
```

Os serviços Python usam Python 3.14 e `pytest`, por exemplo em `analytics/anomaly`:

```sh
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt && .venv/bin/python -m pytest
```

O [CI](.github/workflows/ci.yml) tem três jobs:

- `unit`, com `go vet` e `go test -race`;
- `integration`, que sobe os bancos com o mesmo compose e roda os testes com eles;
- `pytest`, para cada serviço de `analytics/`.

## Estrutura

```
contracts/            JSON Schema e exemplos das mensagens, casos de teste do OEE
internal/             código Go compartilhado: contrato, DLQ, OEE, JSON das APIs, métricas HTTP
services/             serviços Go: simulator, bridge, telemetry, maintenance, kafka-lag
analytics/            serviços Python: anomaly e etl-oee
deploy/               docker-compose.yml, configuração da infra, migrations, dashboards e teste de carga
docs/                 ADRs e resultados do teste de carga
edge/, firmware/      gateway do Pi 5 e firmware do micro:bit (Fase 4)
```

## Próximos passos

- **Hardware real.**
  - Fase 4: um micro:bit mede vibração e temperatura e conta peças nos botões. Um gateway em Go no Raspberry Pi 5 lê a serial e publica no MQTT.
  - Fase 5: Modbus RTU entre os dois e um buffer offline em SQLite no gateway, para a rede cair por minutos sem perder leituras.
- **Evoluções anotadas:**
  - Regras de alerta com aviso, que precisam de Alertmanager ou de um contact point do Grafana.
  - Mais instâncias do `anomaly` no mesmo consumer group, até uma por partição.
  - Uma métrica para os descartes do Mosquitto.
  - Uma tabela de última leitura para o `GET /machines`, que hoje agrupa a tabela inteira.
  - Um usuário só de leitura no ClickHouse para o Grafana.
  - Um limite absoluto de temperatura para a deriva lenta, que o z-score não pega.
- **Fora de escopo, de propósito:** Kubernetes, cloud, frontend próprio, autenticação na API e cluster Kafka com réplicas. O objetivo é um sistema que roda inteiro numa máquina e se explica em uma conversa.

## Licença

[MIT](LICENSE)
