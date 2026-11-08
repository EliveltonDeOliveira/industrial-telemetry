# 0012. Regras de anomalia medidas com dados reais

- Status: aceita
- Data: 2026-11-08
- Muda a regra de z-score do planejamento, que alertava quando |z| > 3.

## Contexto

O plano previa z-score em janela móvel de 60 leituras por máquina, com alerta quando |z| > 3, parada longa acima de 5 minutos e cooldown de 10 minutos. Antes de implementar o `anomaly`, as regras foram testadas com 20 horas-máquina de leituras reais do simulador, exportadas da `telemetry_raw`, e com 183 falhas sintéticas por sensor. As falhas copiam o `POST /faults`: temperatura subindo até 35 °C acima do normal, com constante de tempo de 3 minutos, ou vibração 4 vezes maior, por 120 segundos.

A regra do plano deu cerca de 5 alertas falsos por hora por máquina em cada sensor. O ruído normal passa de 3 desvios em 0,13% das leituras, e a 1 leitura por segundo isso dá perto de 5 por hora. Além disso, |z| alerta a queda de temperatura quando a máquina para e o salto de vibração quando ela volta a rodar. Cada alerta falso abriria uma ordem de manutenção e ainda ocuparia o cooldown, escondendo uma falha real logo depois.

## Decisão

- **Temperatura e vibração:** a leitura alerta quando passa de média + 3 desvios padrão das 60 leituras anteriores do mesmo sensor, por 3 leituras seguidas. Só para cima, porque os tipos são `temperatura_alta` e `vibracao_alta`. Só leituras com `state` igual a `running` entram na janela e são avaliadas, porque parar e voltar a rodar mudam os dois sensores sem que haja falha. Uma leitura parada zera a contagem de leituras seguidas.
- No alerta de sensor, `value` é a leitura e `threshold` é o limite que ela passou (média + 3 desvios).
- **Parada longa:** alerta quando a máquina está parada há mais de 300 segundos, contados a partir da primeira leitura parada. `value` é o tempo parado em segundos.
- **Cooldown:** a mesma máquina não alerta o mesmo tipo de novo antes de 600 segundos. Vale para todos os tipos, então uma parada de 1 hora gera um alerta a cada 10 minutos, todos ligados à mesma ordem aberta no `maintenance`.
- **Severidade fixa por tipo:** `temperatura_alta` é `alta`, `vibracao_alta` e `parada_longa` são `media`. O z-score não serve para graduar, porque uma subida lenta sempre dispara logo acima de 3 e um salto dispara muito acima. `baixa` fica para regras futuras.
- **Tempo do evento:** janela, parada e cooldown usam o `ts` das leituras, não o relógio do serviço, e o `ts` do alerta é o da leitura que o disparou. Reprocessar as mesmas leituras gera os mesmos alertas.
- **`alert_id` derivado da leitura:** UUID versão 5 de `machine_id`, `boot_id`, `seq` e tipo. Uma leitura consumida de novo gera o mesmo `alert_id`, e o `ON CONFLICT DO NOTHING` do `maintenance` descarta a repetição.
- Leitura com `seq` menor ou igual ao último visto no mesmo `boot_id` é descartada, como no `telemetry` (ADR 0003), para uma reentrega não contar duas vezes na janela.
- O produtor Python particiona por murmur2, o mesmo hash do Java e do franz-go, para a mesma `machine_id` cair na mesma partição seja qual for a linguagem do produtor. O padrão do librdkafka usa CRC32, e no teste mandou para partições diferentes o registro de DLQ do `anomaly` e o do `telemetry` com a mesma chave.
- Os valores são configuráveis por `ANOMALY_WINDOW`, `ANOMALY_Z_THRESHOLD`, `ANOMALY_CONSECUTIVE`, `ANOMALY_STOP_S` e `ANOMALY_COOLDOWN_S`.

## Medição

Alertas falsos contados nas leituras reais, com cooldown. Detecção contada nas falhas sintéticas, nos 120 segundos de cada uma. A medição é refeita com `analytics/anomaly/tools/evaluate_rules.py`, que explica como exportar as leituras.

| Regra (janela de 60) | Alertas falsos por hora por máquina, temperatura e vibração | Falhas de temperatura detectadas (mediana) | Falhas de vibração detectadas (mediana) |
| --- | --- | --- | --- |
| \|z\| > 3, todas as leituras (plano) | 4,8 e 5,0 | 95% (5 s) | 100% (0 s) |
| z > 3, só rodando, 1 leitura | 3,7 e 3,5 | 92% (5 s) | 100% (0 s) |
| **z > 3, só rodando, 3 seguidas (escolhida)** | **0,05 e 0** | **81% (9 s)** | **100% (2 s)** |
| z > 3, só rodando, 3 seguidas, leituras acima fora da janela | 0,05 e 0 | 91% (8 s) | 100% (2 s) |

Janelas de 300 e 600 leituras não melhoraram a detecção de temperatura (79% e 86%). Passadas as mesmas 20 horas pelo `Detector` de produção, saiu 1 alerta falso, os mesmos 0,05 por hora da regra escolhida.

## Alternativas consideradas

- **Regra do plano, |z| > 3 em todas as leituras.** Simples, mas com cerca de 10 alertas falsos por hora por máquina somando os dois sensores.
- **Leituras acima do limite fora da janela.** Detecta 91% das falhas de temperatura, porque a janela não aprende a falha. Mas se o nível muda de vez, como depois de trocar uma peça ou mudar o sensor de lugar, a janela congela e o alerta se repete a cada 10 minutos para sempre.
- **Janela maior.** Não melhorou a detecção e demora mais para se adaptar a uma mudança normal.
- **Severidade pelo valor de z.** Não mede urgência, como explicado acima.
- **Um alerta por parada.** Precisaria de uma regra própria só para a parada, e os repetidos já se juntam na mesma ordem.

## Consequências

- Um salto de vibração alerta em 2 segundos e uma subida de temperatura em cerca de 9, com 1 alerta falso a cada 20 horas por máquina nos dados do simulador.
- Deriva lenta não é detectada. Das falhas de temperatura, 19% passam sem alerta, a maioria logo depois de uma parada: a janela ainda guarda as leituras quentes de antes da parada, e quando a temperatura passa delas a janela já está cheia da própria subida, que mantém o z perto de 1,7. Visto ao vivo na `maq-01`, que foi de 37 °C a 60 °C sem alerta. Um limite absoluto por máquina, no cadastro `machines`, cobriria esse caso e fica como evolução.
- Um sensor de valores inteiros, como a temperatura do micro:bit, deixa a janela com pouca variação, e 1 grau a mais por 3 leituras já alerta. Revisar na Fase 4.
- O estado fica em memória. Depois de um restart, as janelas se enchem de novo em 60 leituras rodando, a contagem da parada recomeça e o cooldown zera, então um alerta pode se repetir antes de 10 minutos.
- Com mais de uma instância, um rebalance muda as máquinas de instância e o estado delas recomeça na nova dona.
- Um consumer group novo começa no fim do `telemetria.bruta`, para não abrir ordens de manutenção sobre dias de histórico. Depois disso, vale o offset confirmado.
- Python 3.14 na imagem e no CI, porque o `confluent-kafka` ainda não tem wheels para 3.15.
- O cálculo custa 84 µs por leitura, cerca de 12 mil leituras por segundo num núcleo, contra as 500 por segundo do teste de carga da Fase 3.
