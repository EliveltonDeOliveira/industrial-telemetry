# 0017. Métricas de latência, requisições e lag

- Status: aceita
- Data: 2026-11-08

## Contexto

A Fase 3 pede mensagens por segundo, erros, latência e lag por consumer group em todos os serviços, e o teste de carga vai medir a latência p95. Cada consumidor já contava o que consumiu, as falhas e o que mandou para a DLQ. Faltavam quatro coisas: nenhum serviço media latência, as APIs não mediam as requisições, o lag dos consumer groups não era medido em lugar nenhum e o `etl-oee` não tinha HTTP.

## Decisão

- **Vazão e erros:** continuam nos contadores de cada serviço, como `telemetry_records_consumed_total` e `maintenance_failures_total`, lidos com `rate()`. O fim de cada partição, medido pelo `kafka-lag`, dá também as mensagens por segundo de cada tópico.
- **Requisições HTTP:**
  - O histograma `http_request_duration_seconds{method, route, code}` tem o mesmo nome em todo serviço Go, medido pelo middleware `internal/httpmetrics` em volta do `ServeMux`. O label `job` do Prometheus diz de qual serviço é.
  - `route` é o padrão que o mux escolheu, como `GET /work-orders/{id}`, então ids no caminho não criam séries novas. Um caminho sem rota vira `unmatched`.
  - O `_count` dá requisições por segundo, e o `code` dá os erros.
- **Latência do pipeline:** histogramas medidos a partir do `ts` da leitura, o que mostra a demora que quem olha o dashboard sente, incluindo a fila no Kafka.
  - `telemetry_ingest_delay_seconds`: da leitura até a gravação no ClickHouse. É o p95 do teste de carga.
  - `anomaly_alert_delay_seconds`: da leitura até o alerta publicado.
  - `maintenance_alert_delay_seconds`: da leitura que disparou o alerta até a ordem gravada no PostgreSQL.
- **Lag:**
  - O serviço `kafka-lag`, em Go com o `kadm` do franz-go, lê o Kafka a cada coleta do Prometheus e expõe quatro métricas:
    - `kafka_consumergroup_lag{group, topic, partition}`.
    - `kafka_consumergroup_members{group}`.
    - `kafka_partition_end_offset{topic, partition}`.
    - `kafka_lag_up`, que fica 0 quando a leitura do Kafka falha e assim separa Kafka fora de exportador fora.
  - Ele mede de fora dos consumidores e pega todos os groups, sem lista para manter.
- **`etl-oee`:**
  - Ganha HTTP em `:8086`. O `/healthz` responde 503 quando a última execução falhou.
  - O `/metrics` expõe `etl_oee_runs_total{result}`, `etl_oee_rows_written_total`, `etl_oee_run_duration_seconds` e `etl_oee_last_success_timestamp_seconds`.

## Alternativas consideradas

- **Imagem pronta `danielqsj/kafka-exporter`.** Sem código nosso, mas é uma imagem nova, feita com outro cliente Kafka (sarama), e a compatibilidade com o Kafka 4.4 teria de ser testada. O `kadm` é do mesmo franz-go que os serviços já usam.
- **Cada consumidor medir o próprio lag.** O código se repetiria em Go e em Python, e um consumidor parado deixaria de reportar justamente quando o lag cresce.
- **Latência só por etapa**, como a duração do insert. Mede um salto, não a demora até o dado aparecer, e não enxerga o tempo na fila do Kafka.
- **Summary no lugar de histograma.** Calcula o quantil no próprio serviço, mas quantis de instâncias diferentes não se somam. Com histograma, o `histogram_quantile` junta as instâncias.
- **O caminho da requisição como label.** Cada id de ordem viraria uma série nova.
- **Pushgateway para o `etl-oee`.** Seria mais um serviço de infraestrutura. Como o ETL fica rodando entre as execuções, a coleta normal funciona.

## Consequências

- Medido em 2026-11-08 com as 3 máquinas do simulador:
  - Ingestão com p95 de 1,8 s e mediana de 0,57 s, dominadas pelo lote de 1 segundo do `telemetry`.
  - 0,19 s da leitura até o alerta publicado e 0,20 s até a ordem gravada.
  - `telemetria.bruta` recebeu 3 mensagens por segundo, medidas pelo `kafka-lag`, e o `telemetry` consumiu 2,98 por segundo.
- A latência inclui a diferença entre o relógio de quem preenche o `ts` e o do servidor. No compose os dois são a mesma máquina. Na Fase 4, o `ts` vem do gateway no Pi 5, que precisa de NTP.
- Reprocessar leituras antigas, por exemplo voltando um group ao início, aparece como latência alta. Ela é real: a leitura é velha.
- Uma partição sem offset confirmado conta inteira como lag, então um group novo começa com lag alto até o primeiro commit.
- Com 3 máquinas, só as partições 1 e 4 de `telemetria.bruta` recebem dados, porque as chaves `maq-01` a `maq-03` caem nelas. O teste de carga, com 500 máquinas, espalha a carga pelas 6.
- Os serviços Python só servem `/healthz` e `/metrics`, então não medem requisições.
