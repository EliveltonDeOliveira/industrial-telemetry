# 0003. Idempotência por `boot_id` e `seq`

- Status: aceita
- Data: 2026-11-07

## Contexto

A entrega é pelo menos uma vez em todo o caminho. O MQTT com QoS 1 reentrega o que não foi confirmado, e os consumidores Kafka só confirmam o offset depois de gravar no banco, então uma queda entre a gravação e a confirmação faz a mensagem ser processada de novo. Duplicata precisa ser inofensiva.

O micro:bit não tem relógio nem armazenamento que sobreviva ao desligar, então contadores e sequência voltam a zero a cada reinício. Um `seq` sozinho fica ambíguo depois de um reinício.

## Decisão

- Toda leitura é identificada por `(machine_id, boot_id, seq)`. O `boot_id` é um número aleatório de 32 bits sorteado a cada vez que a máquina liga, e o `seq` cresce dentro dele.
- O `ts` é definido uma vez na origem (simulador ou gateway) e nunca é recalculado numa reentrega.
- Primeira camada: o `telemetry` guarda em memória o último `seq` de cada `(machine_id, boot_id)` e descarta o que for menor ou igual.
- Segunda camada: `telemetry_raw` é `ReplacingMergeTree` com `ORDER BY (machine_id, ts, boot_id, seq)`. Uma duplicata que escape da primeira camada, por exemplo depois de o serviço reiniciar e perder a memória, vira uma linha idêntica que o ClickHouse remove no merge.
- Contadores só são comparados dentro do mesmo `boot_id`. Se diminuírem, é reinício, nunca peça negativa.
- Alertas usam `alert_id` (UUID) como chave primária no PostgreSQL e são gravados com `ON CONFLICT DO NOTHING`.

## Alternativas consideradas

- **Exactly-once do Kafka, com transações.** Cobre só o trecho dentro do Kafka. Não cobre o MQTT nem a gravação no banco sem um protocolo de duas fases, e traz uma complexidade que a idempotência resolve de forma mais simples.
- **UUID por leitura.** Identifica a mensagem, mas não dá ordem nem mostra perda. Com `seq`, um buraco na sequência indica leitura perdida.
- **Guardar o `seq` na flash do micro:bit.** Sobreviveria ao reinício, mas desgasta a memória e complica o firmware. O `boot_id` resolve o mesmo problema sem escrita persistente.

## Consequências

- Reentrega em qualquer ponto do caminho não altera o resultado.
- Buracos em `seq` dentro de um `boot_id` medem perda de dados.
- A remoção de duplicatas do ClickHouse acontece no merge, sem hora marcada. Consultas que precisam de resultado exato usam `FINAL` ou agregam de um jeito que a duplicata não altere, como o máximo de um contador.
- A chance de dois boots da mesma máquina sortearem o mesmo `boot_id` é de 1 em cerca de 4 bilhões, desprezível aqui.
- Na Fase 5, com Modbus, o gateway lê registradores em vez de receber mensagens numeradas, então passa a gerar o `seq` e a guardá-lo no SQLite.
