# 0004. Dead letter queue

- Status: aceita
- Data: 2026-11-07

## Contexto

Um consumidor Kafka processa cada partição em ordem. Se uma mensagem falha sempre, o consumidor fica preso nela e todas as máquinas daquela partição param. Descartar a mensagem em silêncio também não serve: perde dado e esconde o problema.

Ao mesmo tempo, nem toda falha é culpa da mensagem. Se o banco está fora do ar, todas falham, e mandar todas para a DLQ esvaziaria o tópico principal com dados bons.

## Decisão

- O tópico `telemetria.dlq`, com 6 partições e chave `machine_id`, recebe as mensagens que não podem ser processadas. O motivo vai no header `error`, com o texto do erro (por exemplo `seq: missing`).
- Cada falha tem um tratamento:
  - **Mensagem inválida pelo contrato:** vai direto para a DLQ, sem retentativa, porque uma mensagem inválida não fica válida. Vale para a `bridge` e para os consumidores.
  - **Banco ou broker indisponível** (falha de conexão ou timeout): não vai para a DLQ. O consumidor pausa, tenta de novo com backoff sem confirmar o offset e retoma de onde parou quando o banco volta.
  - **Qualquer outra falha**, como alerta de máquina não cadastrada: 3 tentativas com backoff exponencial, depois DLQ. O offset é confirmado e o consumidor segue.
- Ninguém consome a DLQ automaticamente. Ela é acompanhada por métrica no Prometheus e inspecionada à mão. Para reprocessar, a mensagem é publicada de novo no tópico de origem depois de corrigida a causa.

## Alternativas consideradas

- **Descartar e registrar em log.** Simples, mas a mensagem se perde e o log não serve para reprocessar.
- **Retentar para sempre.** Uma mensagem com defeito trava a partição.
- **Tópicos de retry com atraso** (por exemplo, um para 5 segundos e outro para 1 minuto). Padrão comum em sistemas grandes, mas multiplica tópicos e consumidores. Para este volume, retentar em memória antes da DLQ basta.
- **Uma DLQ por tópico.** Facilita reprocessar, mas uma só simplifica o monitoramento, e o volume esperado de falhas é baixo.

## Consequências

- Uma mensagem ruim não para as outras máquinas da partição, e nenhuma falha some sem rastro.
- Uma queda do banco atrasa o processamento, mas não perde nem desvia mensagens. O atraso aparece como lag do consumer group.
- Classificar o erro corretamente importa: um erro de infraestrutura tratado como erro de mensagem mandaria dado bom para a DLQ.
- Uma mensagem na DLQ fura a ordem da sua máquina, porque as seguintes continuam passando. Para telemetria isso é aceitável, já que cada leitura carrega `ts` e `seq`.
- Como a `bridge` e os consumidores usam a mesma DLQ, reprocessar exige saber o tópico de origem. A Fase 1 define um header para isso junto com o `error`.
