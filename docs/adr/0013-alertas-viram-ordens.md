# 0013. Alertas viram ordens de manutenção

- Status: aceita
- Data: 2026-11-08
- Detalha, para o `maintenance`, o caminho "qualquer outra falha" do ADR 0004.

## Contexto

O `maintenance` consome `alertas`, grava cada alerta em `alerts` e liga o alerta a uma ordem em `work_orders`. O índice único parcial garante uma ordem ativa (`aberta` ou `em_andamento`) por máquina e tipo. O mesmo problema chega várias vezes: `parada_longa` repete a cada 10 minutos, um restart do `anomaly` zera o cooldown, e uma reentrega do Kafka traz o mesmo `alert_id` (ADR 0012). Na tarefa seguinte, um `PATCH` da API vai mudar o estado das ordens ao mesmo tempo em que os alertas chegam.

O ADR 0004 manda o "banco fora do ar" repetir sem fim e "qualquer outra falha" ter 3 tentativas antes da DLQ, mas não diz como separar uma da outra.

## Decisão

- **Uma transação por alerta:**
  1. Busca a ordem ativa da máquina e do tipo com `SELECT ... FOR UPDATE`.
  2. Sem ordem ativa, abre uma e registra o evento de criação (`null` para `aberta`).
  3. Grava o alerta ligado à ordem com `ON CONFLICT (alert_id) DO NOTHING`.
  4. Se o `alert_id` já existia, é reentrega e a transação faz rollback, desfazendo a ordem aberta no passo 2, se houve.
- **`FOR UPDATE` contra o `PATCH`:** um alerta que chega enquanto um `PATCH` fecha a ordem espera o `PATCH` terminar e lê o estado novo. A ordem fechada deixa de ser ativa, e o alerta abre outra em vez de entrar numa ordem concluída.
- `opened_at` da ordem e `created_at` do evento são o momento do processamento. O momento da detecção fica em `alerts.ts`.
- **Separação das falhas:**
  - **Rejeição:** um erro do PostgreSQL com SQLSTATE de classe 22 (dado inválido) ou 23 (violação de restrição) é culpa do alerta. O caso esperado é 23503, alerta de máquina fora do cadastro. Tem 3 tentativas, com espera de 1 e 2 segundos, e depois vai para a DLQ com `source` `kafka:alertas` e `error` no formato `postgres: <mensagem>: <detalhe> (SQLSTATE 23503)`.
  - **Infraestrutura:** qualquer outro erro, como conexão recusada, timeout, servidor desligando ou deadlock, é tratado como banco indisponível. Repete sem fim, com espera dobrando até 30 segundos, sem confirmar o offset.
- Só as rejeições contam para as 3 tentativas. Uma falha de conexão no meio delas não manda o alerta para a DLQ.
- **Começo do tópico:** um consumer group novo lê `alertas` desde o início, ao contrário do `anomaly`. Cada alerta já é uma decisão tomada, e pular um seria perder uma ordem. Os alertas publicados antes da primeira subida do `maintenance` também viram ordens, dentro da retenção do Kafka.

## Alternativas consideradas

- **Lote numa transação só.** Mais rápido, mas um alerta rejeitado desfaria o lote inteiro. O volume de alertas é baixo, então uma transação por alerta não pesa.
- **Consultar o `alert_id` antes de abrir a ordem.** É uma consulta a mais, e o `ON CONFLICT` com rollback continuaria necessário para o caso de duas gravações ao mesmo tempo.
- **Todo erro desconhecido como rejeição.** Um erro inesperado durante uma queda do banco mandaria alertas bons para a DLQ. Do jeito escolhido, o pior caso é a partição parada, que aparece no lag e nos logs.
- **Isolamento `SERIALIZABLE` no lugar do `FOR UPDATE`.** Também resolve a disputa com o `PATCH`, mas obriga a repetir a transação em falha de serialização.
- **`opened_at` igual ao `ts` do alerta.** Com o banco fora por uma hora, a ordem pareceria aberta antes de existir no sistema.

## Consequências

- Reentrega e reprocessamento não duplicam alerta, ordem nem evento. Testado ao vivo voltando o consumer group ao início do tópico.
- Uma reentrega depois de a ordem fechar não reabre nada. Um alerta novo do mesmo tipo, com outro `alert_id`, abre uma ordem nova: uma parada que continua depois de a ordem ser concluída volta a gerar ordem.
- Um alerta rejeitado segura a partição por cerca de 3 segundos antes da DLQ.
- Para reprocessar um alerta de máquina fora do cadastro, cadastre a máquina e publique a mensagem da DLQ de novo em `alertas`.
- Uma migration que não rodou (tabela inexistente, classe 42) conta como infraestrutura: o consumidor espera em vez de mandar tudo para a DLQ.
- O `id` das ordens pode ter buracos, porque o rollback e as rejeições consomem valores da sequência.
- Os testes de integração em `services/maintenance/store_test.go` cobrem a disputa com o `PATCH`: sem o `FOR UPDATE`, o teste falha.
