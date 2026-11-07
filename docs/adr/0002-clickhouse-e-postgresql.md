# 0002. ClickHouse para telemetria e PostgreSQL para manutenção

- Status: aceita
- Data: 2026-11-07

## Contexto

O sistema tem duas cargas bem diferentes:

- **Telemetria:** só inserção, volume alto (o teste de carga mira 500 máquinas a 1 leitura por segundo, cerca de 43 milhões de linhas por dia) e consultas que agregam janelas de tempo por máquina, como OEE e gráficos.
- **Manutenção:** volume baixo, atualizações, transições de estado e regras de integridade (uma ordem ativa por máquina e tipo, chave estrangeira para máquinas, alerta sem duplicata).

## Decisão

- ClickHouse para `telemetry_raw` e `oee_turno`.
- PostgreSQL para `machines`, `alerts`, `work_orders` e `work_order_events`.

## Alternativas consideradas

- **Só PostgreSQL, com TimescaleDB.** Um banco só para operar, e daria conta desse volume. Ficou de fora porque o ClickHouse comprime melhor e agrega mais rápido quando a telemetria chega a bilhões de linhas, e porque o objetivo do projeto inclui mostrar a separação entre carga analítica e transacional.
- **Só ClickHouse.** Não tem transação, chave estrangeira nem índice único, e atualizar uma linha é uma mutação assíncrona. Ruim para ordens de serviço.
- **Banco de séries temporais, como InfluxDB.** Bom para métricas, mas com SQL limitado e sem joins, o que dificulta o cálculo de OEE.

## Consequências

- Cada carga fica no motor feito para ela, e os dois falam SQL.
- São dois bancos para operar, com migrations separadas (ADR 0007).
- Os bancos não fazem join entre si: o `etl-oee` lê `ideal_cycle_s` do PostgreSQL e a telemetria do ClickHouse, e combina os dois em Python. Isso mantém os bancos desacoplados.
- A remoção de duplicatas no ClickHouse é eventual (ADR 0003), então consultas que precisam de resultado exato usam `FINAL`.
