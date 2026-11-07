# 0008. Headers da DLQ

- Status: aceita
- Data: 2026-11-07

## Contexto

O ADR 0004 criou um tópico `telemetria.dlq` compartilhado pela `bridge` e pelos consumidores, com o motivo da falha no header `error`. Para reprocessar uma mensagem, é preciso saber também de onde ela veio, e a origem pode ser um tópico MQTT ou um tópico Kafka. A mensagem pode chegar à DLQ justamente por não ser um JSON válido.

## Decisão

- O valor do registro na DLQ é a mensagem original, byte a byte, sem envelope.
- A chave é o `machine_id`. Na `bridge`, ele sai do tópico MQTT (`fabrica/{machine_id}/telemetria`), porque o payload pode não ter um `machine_id` válido.
- Três headers, em texto:
  - `error`: o motivo, começando pelo nome do campo, por exemplo `seq: missing`.
  - `source`: a origem, com o transporte na frente, como `mqtt:fabrica/maq-01/telemetria` ou `kafka:telemetria.bruta`.
  - `service`: o serviço que desistiu da mensagem, como `bridge` ou `telemetry`.
- Reprocessar é publicar o valor de novo no `source`, depois de corrigida a causa.
- Os nomes ficam em constantes no pacote `internal/dlq`, usado por todos os serviços Go.

## Alternativas consideradas

- **Envelope JSON**, com a mensagem original dentro e os metadados ao lado. Fica tudo no corpo, mas uma mensagem que não é JSON precisaria virar string escapada, e reprocessar exigiria desembrulhar.
- **Só o tópico de origem, sem o transporte.** Os nomes de tópico do MQTT e do Kafka têm formatos diferentes, mas deixar o transporte explícito evita adivinhar.
- **Partição e offset de origem no header.** Ajudariam a rastrear mensagens que vieram do Kafka, mas não servem para as que vieram do MQTT e não são necessários para reprocessar.

## Consequências

- Uma mensagem malformada chega à DLQ exatamente como o dispositivo mandou.
- O `kafka-console-consumer.sh` com `print.headers=true` mostra motivo, origem e mensagem, sem ferramenta própria.
- O header `error` é texto livre. Uma métrica por motivo precisa extrair o nome do campo antes do `:`.
- Se a mesma mensagem falhar em dois consumidores, ela aparece duas vezes na DLQ, uma por `service`.
