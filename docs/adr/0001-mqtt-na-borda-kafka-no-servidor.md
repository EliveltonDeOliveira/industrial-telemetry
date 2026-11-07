# 0001. MQTT na borda e Kafka no servidor

- Status: aceita
- Data: 2026-11-07

## Contexto

As máquinas, simuladas ou o micro:bit atrás do gateway no Pi 5, publicam leituras pequenas e frequentes a partir de uma rede que pode cair. No servidor, a mesma leitura precisa chegar a consumidores independentes (`telemetry` grava no ClickHouse, `anomaly` procura anomalias), cada um no seu ritmo, com a ordem preservada por máquina e com a possibilidade de reprocessar o histórico.

## Decisão

- Na borda, MQTT com o Mosquitto. Cada máquina publica em `fabrica/{machine_id}/telemetria` com QoS 1.
- No servidor, Kafka em modo KRaft. O serviço `bridge` assina os tópicos MQTT, valida o contrato e publica em `telemetria.bruta` com chave `machine_id`.
- Cada tópico Kafka tem 6 partições, e cada serviço consome com o seu próprio consumer group.

## Alternativas consideradas

- **Só MQTT.** Mais simples, mas o broker MQTT não guarda histórico: depois de entregue, a mensagem some. Não dá para reprocessar, e um consumidor novo começa do zero.
- **Gateway publicando direto no Kafka.** O Pi 5 aguentaria um cliente Kafka, mas o protocolo foi pensado para a rede do datacenter: o cliente precisa alcançar cada broker pelo endereço anunciado, e cada dispositivo vira um cliente do cluster. O MQTT foi feito para dispositivos pequenos em rede instável (keepalive, QoS, sessão persistente) e é o padrão de fato em IoT.
- **Kafka Connect com conector MQTT.** Evita escrever a bridge, mas traz mais um componente JVM para operar, e a validação do contrato e o envio para a DLQ ficariam em configuração em vez de código testável.

## Consequências

- A borda fica leve: cliente MQTT simples, QoS 1 e reconexão automática.
- O Kafka guarda o log, então um consumidor novo ou corrigido pode reprocessar desde o início. A chave `machine_id` garante a ordem das leituras de uma máquina dentro da partição.
- Com 6 partições, cada serviço pode escalar até 6 instâncias no mesmo consumer group.
- São dois brokers para operar, e a `bridge` vira um ponto do caminho que precisa ser monitorado.
- Uma reentrega do MQTT pode virar mensagem duplicada no Kafka. Isso é tratado pela idempotência (ADR 0003).
