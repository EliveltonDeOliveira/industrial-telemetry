# Teste de carga

Medido em 2026-11-08 com o compose de `deploy/`, num notebook com 12 threads e 16 GB de RAM, junto com Grafana e Prometheus. O método e as decisões estão no [ADR 0019](adr/0019-teste-de-carga.md).

## Resumo

- **500 máquinas, a 1 leitura por segundo cada, por 30 minutos:** 503 leituras por segundo em todas as etapas. A ingestão teve p95 de 0,98 segundo e o lag máximo foi de cerca de 1 segundo de leituras. Não houve falha nem mensagem perdida, e o pipeline inteiro usou 1 núcleo.
- **Primeiro gargalo, a 1600 leituras por segundo:** a janela de 20 mensagens em voo do Mosquitto para a bridge. Acima disso, a fila no Mosquitto encheu e ele descartou 64.843 leituras sem que nenhuma métrica do projeto contasse. Com a janela em 1000, a bridge passou de 10.000 leituras por segundo.
- **Segundo gargalo, a 8000 leituras por segundo:** o `anomaly`, um processo Python numa thread só, chega a 100% de um núcleo. Acima disso, o lag dele cresce e os alertas atrasam, mas nada se perde, porque o Kafka guarda as leituras.
- **ETL:** com 500 máquinas e três turnos inteiros, uma execução leu 43,2 milhões de leituras e levou 63 segundos, bem dentro da hora entre execuções.

## Como repetir

Em `deploy/`, com a stack no ar:

```sh
loadtest/machines.sh add 500                         # cadastra carga-01 a carga-500
CARGA_MACHINES=500 docker compose up -d simulator-carga
# 3 minutos de aquecimento, depois a janela medida
loadtest/report.py --minutes 30                      # números da janela, lidos do Prometheus
CARGA_MACHINES=0 docker compose up -d simulator-carga
# espere o lag do anomaly e do maintenance voltar a perto de 0 no dashboard "Plataforma"
loadtest/machines.sh remove
```

- Kafka, ClickHouse, Mosquitto e PostgreSQL não expõem métricas no Prometheus. A CPU e a memória deles vieram do `docker stats --no-stream`, a cada 10 segundos.
- Os descartes do Mosquitto aparecem no tópico `$SYS/broker/publish/messages/dropped`:
  ```sh
  docker compose exec mosquitto mosquitto_sub -v -t '$SYS/broker/publish/messages/dropped' -C 1
  ```

Para o ETL, o roteiro é este:

```sh
loadtest/machines.sh add 500
loadtest/machines.sh readings 500 32                 # 57,6 milhões de leituras sintéticas
docker compose run --rm etl-oee --once               # o log "oee written" traz duration_s
loadtest/machines.sh remove
```

## 500 máquinas por 30 minutos

Janela de 21:13 a 21:43 UTC, depois de 3 minutos de aquecimento.

| Medida | Resultado |
| --- | --- |
| Vazão: simulators, bridge, Kafka, `telemetry` e `anomaly` | 503 leituras por segundo em todas |
| Ingestão, da leitura ao ClickHouse | p50 de 0,53 s, p95 de 0,98 s e p99 de 1,70 s |
| Lag máximo e médio do `telemetry` | 510 e 260 mensagens |
| Lag máximo e médio do `anomaly` | 528 e 274 mensagens |
| Lag do `maintenance` | 0 |
| Da leitura ao alerta e à ordem, com `vibracao_alta` injetada na `maq-01` | 0,30 s e 0,31 s, contra 0,19 s e 0,20 s sem carga |
| Falhas, DLQ e descartes do Mosquitto | nenhum |
| CPU média | 1,0 núcleo no total: Kafka 0,32, bridge 0,17, `simulator-carga` 0,13, ClickHouse 0,10, `anomaly` 0,10 e `telemetry` 0,09 |
| Memória máxima | Kafka 988 MiB, ClickHouse 561 MiB e cada serviço do projeto abaixo de 45 MiB |
| `GET /machines` com 504 máquinas e 1,1 milhão de leituras | de 14 a 23 ms |

## Degraus

Os degraus mediram 5 minutos cada, depois de 1 minuto de aquecimento. O de 10.000 máquinas mediu 3 minutos. Até a primeira linha de 2000 máquinas, o Mosquitto tinha a janela padrão de 20 mensagens em voo; dali em diante, a de 1000.

| Máquinas | Janela | Bridge recebeu | ClickHouse gravou | `anomaly` consumiu | Ingestão p95 | Lag máximo do `telemetry` e do `anomaly` | CPU do `anomaly` | CPU total |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1000 | 20 | 1003/s | 1003/s | 1003/s | 0,97 s | 1012 e 577 | 0,17 | 1,3 |
| 2000 | 20 | 1584/s | 1586/s | 1582/s | entre 60 e 300 s | 1003 e 580 | 0,25 | 1,7 |
| 2000 | 1000 | 2003/s | 2003/s | 2003/s | 0,50 s | 999 e 692 | 0,33 | 1,7 |
| 4000 | 1000 | 4003/s | 4003/s | 4003/s | 0,34 s | 1044 e 895 | 0,61 | 2,1 |
| 6000 | 1000 | 6003/s | 6003/s | 6003/s | 0,24 s | 1448 e 947 | 0,79 | 2,2 |
| 8000 | 1000 | 8003/s | 8003/s | 7995/s | 0,24 s | 1503 e 3794, crescendo | 1,00 | 2,5 |
| 10.000 | 1000 | 10.003/s | 10.000/s | 7970/s | 0,24 s | 1408 e 392.377 | 0,99 | 2,7 |

Os quantis vêm dos baldes dos histogramas (0,1, 0,25, 0,5, 1, 2, 5, 10, 30, 60 e 300 segundos) e são interpolados dentro deles, então valem como faixa. No degrau de 2000 com janela de 20, o atraso caiu no balde de 60 a 300 segundos.

## O que os números mostram

### A janela do Mosquitto limitava a bridge

- A bridge só confirma a mensagem MQTT (PUBACK) depois que o Kafka grava, para a entrega ser pelo menos uma vez (ADR 0003). O franz-go espera até 10 ms (`linger`) para juntar registros num lote, então cada confirmação leva cerca de 12,6 ms.
- O Mosquitto só manda 20 mensagens sem confirmação para um cliente (`max_inflight_messages`). Com isso, o teto é de 20 mensagens a cada 12,6 ms, cerca de 1590 por segundo, e a medição deu 1584. A bridge usava só 0,28 núcleo.
- A hipótese foi testada mudando só a janela: com 40, o teto foi a 3373 por segundo. Com 1000, a bridge acompanhou 10.000 por segundo com 0,48 núcleo.
- Quando a bridge não acompanha, a diferença vai para a fila da sessão dela no Mosquitto. Com 2000 máquinas e janela de 20, a fila de 100 mil mensagens (`max_queued_messages`) encheu em cerca de 4 minutos. Daí em diante, o Mosquitto descartou o que chegava, 64.843 leituras.
- Esse descarte só aparece no log do Mosquitto e em `$SYS/broker/publish/messages/dropped`. O simulator conta a leitura como publicada, porque o broker confirmou, e a bridge nunca a recebe.

### O `anomaly` é o próximo limite

- O `anomaly` é um processo Python. Ele interpreta o JSON, valida o contrato e atualiza a janela de cada máquina numa thread só. Chegou a 100% de um núcleo com cerca de 8000 leituras por segundo.
- Com 10.000 máquinas, o lag dele subiu cerca de 2000 mensagens por segundo, até 392 mil. Os alertas atrasam na mesma medida, mas nada se perde. Ao fim da carga, ele consumiu o atraso a cerca de 8600 por segundo, em pouco mais de um minuto.
- Para ir além, mais instâncias no mesmo consumer group dividem as 6 partições, até 6 instâncias. A janela de cada máquina fica na instância dona da partição e é refeita depois de um rebalance, com o aquecimento de 60 leituras do ADR 0012.

### A latência cai com a carga

O lote do `telemetry` fecha com 1000 linhas ou 1 segundo, o que vier primeiro. Até 1000 leituras por segundo, ele espera o segundo inteiro. Acima disso, fecha pelas 1000 linhas, cada vez mais cedo: a cada 0,25 segundo com 4000 leituras por segundo. Por isso o p95 cai de 0,98 segundo com 500 máquinas para 0,24 segundo com 6000.

### O resto sobra

- Com 10.000 leituras por segundo, o `telemetry` usou 0,16 núcleo, a bridge 0,48 e o Kafka cerca de 0,26, que não cresceu com a carga: eram 0,32 com 500 máquinas.
- O ClickHouse ficou abaixo de 0,2 núcleo em média, com picos de meio núcleo.
- Fora o `anomaly`, nenhum serviço do projeto passou de 85 MiB. O `anomaly` chegou a 212 MiB, com a janela de 10.000 máquinas em memória.

### Alertas falsos na troca de degrau

- Cada troca de degrau recria o `simulator-carga`. As máquinas voltam já na temperatura de trabalho, e as que estavam esfriando depois de uma parada dão um salto de temperatura.
- Foram 4351 alertas falsos de `temperatura_alta`, que abriram 3547 ordens. Na subida inicial não houve nenhum, porque o `anomaly` ainda não tinha a janela dessas máquinas.
- O `maintenance` gravou até 32 alertas por segundo sem lag. Da leitura até a ordem, foram 0,38 s na mediana e 1,6 s no p95.
- É a pendência do simulator: uma máquina que liga deveria começar na temperatura ambiente.

### ETL e `GET /machines` com dados de um dia

- Com 500 máquinas e 32 horas de leituras sintéticas, uma por segundo, uma execução do `etl-oee` calculou 3 turnos encerrados de 504 máquinas.
  - Leu 43,2 milhões de leituras, gravou 1512 linhas em `oee_turno` e levou 63 segundos.
  - O ETL usou 0,8 núcleo e 46 MiB, então o limite é o Python decodificando e calculando.
  - Na mesma proporção, cabem cerca de 28.000 máquinas na hora entre duas execuções.
- O `GET /machines` agrupa a `telemetry_raw` inteira para achar a última leitura de cada máquina. Com 57,7 milhões de linhas, levou cerca de 0,32 segundo, e 1,6 segundo na primeira chamada.
  - O tempo cresce com a tabela, e 500 máquinas geram 43 milhões de leituras por dia.
  - Uma tabela de última leitura mantida por materialized view deixaria o tempo constante.
