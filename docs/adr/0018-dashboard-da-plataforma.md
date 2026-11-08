# 0018. Dashboard da plataforma e séries com label criadas em zero

- Status: aceita
- Data: 2026-11-08

## Contexto

O ADR 0017 colocou métricas de vazão, erros, latência e lag em todos os serviços, mas o Grafana ainda não lia o Prometheus. O teste de carga da Fase 3 vai tirar a vazão, o lag e a latência p95 de um dashboard, e o README vai mostrar esses números. Na validação do dashboard, a primeira mensagem que a bridge mandou para a DLQ depois de subir não apareceu nos gráficos.

## Decisão

- **Datasource:** o Prometheus (uid `prometheus`) entra no provisionamento com `timeInterval` de 15 segundos, igual ao `scrape_interval`. Assim o `$__rate_interval` do Grafana cobre sempre pelo menos 4 coletas.
- **Dashboard "Plataforma"** (uid `platform`):
  - Atualiza a cada 15 segundos. Mais rápido que a coleta, ele só repetiria o mesmo valor.
  - Mostra um quadro por alvo do Prometheus e um para o Kafka, pelo `kafka_lag_up`.
- **Números do topo:** resumem o período escolhido, com consultas instantâneas sobre `$__range`:
  - leituras gravadas por segundo, na média do período;
  - ingestão p95;
  - p95 da leitura até a ordem;
  - lag máximo;
  - mensagens na DLQ;
  - falhas.

  No teste de carga, basta escolher a janela do teste. Os gráficos abaixo mostram a evolução, com `rate()` sobre `$__rate_interval`.
- **Seções dos gráficos:**
  - Vazão: mensagens por etapa do pipeline, por tópico e requisições HTTP por rota.
  - Erros: falhas por minuto, DLQ por minuto e por serviço, e respostas 4xx e 5xx.
  - Latência: p50, p95 e p99 da ingestão, e p95 de cada rota HTTP.
  - Kafka: lag por group, uma matriz de lag por partição e consumidores por group.
  - ETL de OEE: tempo desde o último sucesso, execuções, duração média e linhas gravadas.
- **Lag sem cor:** o que é lag demais depende da vazão. Com 3 máquinas, 100 mensagens são meio minuto de leituras. Com 500 máquinas, são um quinto de segundo.
- **Último sucesso do ETL com cor:** fica laranja depois de 65 minutos, que é uma execução perdida, e vermelho depois de 2 horas.
- **Contadores do ETL desde a subida:** em vez de `increase()` em 24 horas. O ETL roda ao subir, antes da primeira coleta, e o `increase()` perderia essa execução.
- **Séries com label criadas em zero:**
  - O Prometheus calcula `rate()` e `increase()` pela diferença entre coletas, então não vê o primeiro incremento de uma série que já aparece em 1.
  - Todo contador com label passa a criar em 0, na subida do serviço, as séries que conhece, como o `etl-oee` já fazia: falhas por operação no `telemetry`, no `maintenance` e no `anomaly`, mensagens por tópico na bridge e alertas por tipo no `anomaly`.
- **Sem regras de alerta:** as cores mostram o problema para quem está olhando o dashboard, mas ninguém recebe aviso.

## Alternativas consideradas

- **Dashboards prontos da comunidade, como os do kafka-exporter.** Usam outros nomes de métricas e não mostram o pipeline etapa por etapa.
- **Números do topo com o valor atual.** No teste de carga, seria preciso procurar o pior momento no gráfico. O p95 e o lag máximo do período já respondem.
- **Média da leitura até a ordem, pela soma dividida pela contagem.** É exata, mas o resto do dashboard e o teste de carga usam o p95. Com poucos alertas, o p95 é interpolado dentro de um balde do histograma: com todos os alertas entre 0,1 e 0,25 segundo, ele mostra 0,24 segundo, e a média medida foi 0,19.
- **Esconder a série que falta no painel, com `or vector(0)`.** O painel deixaria de ficar vazio, mas o primeiro incremento continuaria invisível para o `rate()`.
- **Regras de alerta no Prometheus ou no Grafana.** Sem Alertmanager nem canal de aviso, só repetiriam em outro lugar o que o dashboard já mostra. Ficam como evolução.

## Consequências

- Validado em 2026-11-08, com as 3 máquinas do simulador:
  - Média de 2,99 leituras gravadas por segundo, e ingestão p95 de 1,8 segundo.
  - Com o `telemetry` parado por 45 segundos, o quadro dele ficou fora, o group ficou com 0 consumidores, o lag chegou a 147 mensagens e voltou para 3 em 30 segundos depois da volta.
  - Uma mensagem inválida pelo MQTT apareceu em "Mensagens na DLQ" e na linha da bridge em "DLQ por minuto".
- Um valor novo de label precisa entrar na lista criada na subida. Se ficar de fora, o contador no `/metrics` continua certo, e só o primeiro incremento some dos gráficos.
- Boa parte das requisições ao `telemetry` vem do próprio Grafana, pelo dashboard de OEE. Com ele fechado, quase não há requisições para medir.
- A matriz de lag por partição é montada no navegador, por uma transformação do Grafana. A API de consultas do Grafana só confere a consulta, não a matriz.
