# 0019. Teste de carga com um simulator separado

- Status: aceita
- Data: 2026-11-08

## Contexto

A Fase 3 pede um teste com 500 máquinas a 1 leitura por segundo, medindo vazão, lag e latência p95. O simulator normal nomeia as máquinas `maq-01`, `maq-02`... e, com mais de 3, também publicaria `maq-04`, que é o micro:bit. O cadastro `machines` só tem `maq-01` a `maq-04`, então alertas de outras máquinas iriam para a DLQ depois de cerca de 3 segundos de tentativas cada (ADR 0013), e o `etl-oee` ignoraria essas máquinas. O teste também precisa dizer onde o sistema quebra, poder ser repetido e não deixar dados para trás.

## Decisão

- **`simulator-carga`:**
  - É uma segunda instância do simulator, com `SIM_PREFIX=carga`, que publica `carga-01`, `carga-02`...
  - Fica no ar com 0 máquinas, e o teste só muda `CARGA_MACHINES`. As máquinas `maq-01` a `maq-03` seguem publicando sem reiniciar.
  - O Prometheus coleta o `simulator-carga` como qualquer serviço.
- **Cadastro:** o script de desenvolvimento `deploy/loadtest/machines.sh` faz três coisas, sem migration:
  - `add N` cadastra `carga-01` a `carga-N` com o `ideal_cycle_s` do perfil de cada uma.
  - `readings N H` grava leituras sintéticas.
  - `remove` apaga as máquinas `carga-*` com alertas, ordens, leituras e OEE por turno.
- **Medição:**
  - `deploy/loadtest/report.py --minutes N` lê do Prometheus uma janela do teste:
    - a vazão em cada etapa;
    - p50, p95 e p99 da ingestão;
    - p95 do alerta e da ordem;
    - lag máximo e médio;
    - falhas e DLQ;
    - CPU e memória de cada serviço.
  - Kafka, ClickHouse, Mosquitto e PostgreSQL não expõem métricas no Prometheus, então CPU e memória deles vêm do `docker stats` a cada 10 segundos.
- **Roteiro:**
  - 3 minutos de aquecimento e uma janela de 30 minutos com 500 máquinas.
  - Depois, degraus de 5 minutos com mais máquinas até o lag crescer sem parar, para achar o primeiro gargalo.
- **ETL com turnos inteiros:** um turno tem 8 horas, e o ETL só calcula turnos encerrados. `machines.sh readings` grava leituras sintéticas direto no ClickHouse, uma por segundo e por máquina, nas horas antes da hora atual. Assim o ETL é cronometrado lendo turnos inteiros sem esperar um dia de teste.
- **`max_inflight_messages 1000` no Mosquitto:**
  - O teste achou um teto de cerca de 1600 leituras por segundo na bridge, que vinha do padrão de 20 mensagens em voo do Mosquitto.
  - A bridge só confirma depois que o Kafka grava, cerca de 12,6 ms com o `linger` de 10 ms do franz-go. Com a janela em 40, o teto dobrou, o que confirmou a causa.
  - Com 1000, o teto calculado passa de 70.000 por segundo, e a bridge acompanhou as 10.000 do teste.

## Alternativas consideradas

- **`SIM_MACHINES=500` no simulator normal.** Reiniciaria `maq-01` a `maq-03`, o que já gerou alerta falso de temperatura. Também publicaria `maq-04`, do micro:bit, e a limpeza não conseguiria separar as leituras do teste.
- **Subir o `simulator-carga` só com um profile do compose, achado pelo Prometheus por DNS.** Não haveria alvo fora do ar fora do teste, mas o Prometheus registra um erro a cada 15 segundos enquanto o nome não resolve.
- **Uma ferramenta de carga MQTT pronta.** Mandaria mensagens no tamanho certo, mas sem contadores coerentes nem falhas, e seria mais uma dependência. O simulator já gera leituras válidas pelo contrato.
- **Não cadastrar as máquinas.** Os alertas delas segurariam a partição do `maintenance` durante as tentativas, e o ETL não seria medido.
- **Gerar as leituras do ETL pelo pipeline.** Precisaria de 24 horas de teste com 500 máquinas para ter três turnos encerrados.
- **`linger` 0 no producer da bridge, em vez de uma janela maior.** Cada confirmação cairia para cerca de 2,5 ms, mas o teto continuaria preso à janela de 20, em cerca de 8000 por segundo. Também perderia o lote, que reduz as requisições ao Kafka com carga alta.
- **Janela sem limite (`max_inflight_messages 0`).** Depois de uma queda, a bridge receberia de uma vez toda a fila de até 100 mil mensagens. Com 1000, a reentrega fica limitada e o descarte por `seq` do `telemetry` absorve os repetidos.

## Consequências

- Os números estão em [docs/teste-de-carga.md](../teste-de-carga.md):
  - Com 500 máquinas por 30 minutos, foram 503 leituras por segundo em todas as etapas, ingestão p95 de 0,98 segundo, nenhuma falha e 1 núcleo de CPU.
  - Com a janela em 1000, o pipeline acompanhou 6000 leituras por segundo com ingestão p95 de 0,24 segundo.
  - O `anomaly` satura um núcleo perto de 8000 leituras por segundo.
  - O `etl-oee` leva 63 segundos para 500 máquinas com três turnos inteiros.
  - O `GET /machines` leva 0,32 segundo com 57,7 milhões de leituras.
- Quando a bridge não acompanha ou está fora, a fila da sessão dela no Mosquitto guarda até 100 mil mensagens. São 3,3 minutos com 500 máquinas. Depois disso, o Mosquitto descarta as leituras novas, e esse descarte só aparece no log dele e em `$SYS/broker/publish/messages/dropped`, sem métrica no Prometheus.
- Cada troca de degrau recria o `simulator-carga`, e a pendência do simulator, que religa máquinas já quentes, gerou 4351 alertas falsos de temperatura e 3547 ordens. O `machines.sh remove` apaga tudo isso no PostgreSQL. Os alertas continuam no tópico `alertas` até a retenção do Kafka: voltar o group do `maintenance` ao início manda cada um para a DLQ depois de 3 tentativas.
- O Kafka guarda as leituras do teste em `telemetria.bruta` até a retenção, cerca de 1,2 GB. O `anomaly` guarda a janela das máquinas de carga em memória até reiniciar.
- O teste roda na mesma máquina que todo o resto. O `simulator-carga` usou até meio núcleo, e o número que importa para cada serviço é a CPU dele, não a soma.
