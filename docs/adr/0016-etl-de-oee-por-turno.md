# 0016. ETL de OEE por turno

- Status: aceita
- Data: 2026-11-08

## Contexto

A API do `telemetry` calcula o OEE de qualquer período a partir das leituras brutas (ADR 0009), com no máximo 7 dias por consulta. Para comparar turnos e dias, o `etl-oee` grava a cada hora o OEE de cada máquina por turno em `oee_turno`, e a rota `GET /machines/{id}/oee/shifts` lê de lá. Os turnos são definidos em horas locais da fábrica. O ADR 0009 pede que outra implementação do OEE siga as mesmas regras e passe pelos mesmos casos de teste.

## Decisão

- **Mesma regra em Go e em Python:** o ETL reimplementa o cálculo do ADR 0009 em Python, em `analytics/etl-oee/etl_oee/oee.py`. Os casos de teste saem do teste Go e vão para `contracts/oee/cases.json`, lido pelo `go test` de `internal/oee` e pelo `pytest` do ETL. Um caso novo vale para as duas implementações.
- **Turnos:**
  - Configuráveis por `ETL_SHIFTS`, com padrão `06-14,14-22,22-06`, nas horas locais de `ETL_TIMEZONE`, com padrão `America/Sao_Paulo`.
  - Um turno pode cruzar a meia-noite, e `shift_date` é a data local em que ele começa.
  - Horas fora dos turnos não são tempo planejado, e turnos sobrepostos são recusados na subida.
  - As bordas são calculadas com `zoneinfo`: numa mudança de horário de verão, o turno da noite tem 7 ou 9 horas, e o tempo planejado acompanha.
- **Só turnos encerrados:**
  - Cada execução recalcula os turnos que terminaram nas últimas `ETL_LOOKBACK_H` horas (padrão 24) e grava uma linha por máquina e turno.
  - Uma leitura que chega atrasada entra na execução seguinte, e o `ReplacingMergeTree(computed_at)` fica com o cálculo mais novo.
  - O turno em andamento não entra em `oee_turno`: ele continua ao vivo em `GET /machines/{id}/oee`.
- **Agenda:** o ETL roda ao subir e depois a cada hora, aos 5 minutos, quando as últimas leituras do turno que acabou já estão gravadas. Uma execução que falha não é repetida, porque a próxima cobre os mesmos turnos. Com `--once`, roda uma vez só e serve para backfill, por exemplo `ETL_LOOKBACK_H=72`.
- **Máquina sem leituras no turno:** recebe uma linha com zeros, porque o ADR 0009 conta tempo sem leituras como não rodando.
- **Colunas novas:** `oee_turno` ganha `shift_end`, `planned_s`, `running_s`, `good` e `scrap`, pela migration `0002_oee_turno_details.sql`. Assim a rota de turnos responde no formato do `GET /machines/{id}/oee`, e um fator pode ser conferido a partir dos números que o formam.
- **Rota de turnos:**
  - `GET /machines/{id}/oee/shifts?date=2026-11-08` responde os turnos que começam nessa data local, em ordem, lendo com `FINAL`.
  - `date` é obrigatória, porque a API não conhece o fuso da fábrica.
  - Máquina fora do cadastro dá 404, e uma data sem turnos calculados dá uma lista vazia.

## Alternativas consideradas

- **O ETL chamar a API do `telemetry` para cada turno.** A regra ficaria só em Go, mas o ETL dependeria do serviço de pé e não leria dos bancos, que é o trabalho de um ETL. Com os casos compartilhados, duas implementações não divergem.
- **OEE por turno em SQL no ClickHouse.** Já recusado no ADR 0009.
- **Gravar também o turno em andamento.** Precisaria de um campo para dizer que o número ainda não está fechado, e quem lê `oee_turno` poderia comparar um turno parcial com um completo.
- **Rodar sem horário fixo, a cada 60 minutos desde a subida.** O resultado de um turno poderia demorar até uma hora para aparecer. Aos 5 minutos de cada hora, ele aparece 5 minutos depois do fim.

## Consequências

- No backfill de 72 horas de 2026-11-08, os 36 turnos gravados pelo ETL bateram com a API em Go para o mesmo período, sem diferença nas peças nem nos fatores.
- Um turno é recalculado 24 vezes nas 24 horas depois do fim. Com 4 máquinas, uma execução leva 0,2 segundo. No teste de carga da Fase 3, com 500 máquinas, cada execução lê três turnos de leituras de todas, e o tempo do ETL precisa ser medido.
- Uma leitura que chega mais de `ETL_LOOKBACK_H` horas depois do fim do turno não entra no OEE gravado. A API ao vivo a enxerga.
- Ao mudar `ETL_SHIFTS`, os turnos antigos ficam gravados com os rótulos antigos.
- Para um fuso com deslocamento de meia hora, os turnos terminam aos 30 minutos em UTC e aparecem 35 minutos depois, na execução seguinte.
- O ETL não tem HTTP, então não expõe `/healthz` nem `/metrics`, e a execução só aparece nos logs. As métricas da Fase 3 incluem o ETL.
