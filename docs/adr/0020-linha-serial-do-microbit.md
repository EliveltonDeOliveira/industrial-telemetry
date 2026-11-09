# 0020. Linha serial do micro:bit

- Status: aceita
- Data: 2026-11-08

## Contexto

Na Fase 4, um micro:bit v2 mede vibração e temperatura, conta peças nos botões e manda as leituras pela serial USB. Um gateway em Go no Raspberry Pi 5 lê a serial e publica no MQTT, no mesmo tópico do simulator. O micro:bit não tem relógio e não sabe qual máquina é. O formato da linha é o contrato entre o firmware em C e o gateway, e precisa ser testado como os outros contratos (ADR 0006). Na Fase 5 a serial vira Modbus RTU e o gateway passa a gerar o `seq`, então este formato vale para a Fase 4.

## Decisão

- **Uma linha JSON por leitura:**
  - Uma linha por segundo, a 115200 bauds (8N1), pela UART que o micro:bit liga ao USB.
  - A linha termina em `\n` ou `\r\n`, porque o console do Zephyr manda `\r\n`.
  - Tem até 256 bytes com o fim de linha (`contract.MaxSerialLineBytes`). A maior linha que o firmware imprime tem 173.
- **A linha é a telemetria sem `machine_id` e `ts`:**
  - Os campos são `schema_version`, `boot_id`, `seq`, `state`, `good_total`, `scrap_total`, `temperature_c` e `vibration_rms`, com as regras do contrato de telemetria.
  - O firmware sorteia o `boot_id` com o gerador de números aleatórios do chip cada vez que liga. O `seq` e os contadores começam em 0.
  - O firmware imprime as chaves nessa ordem, `temperature_c` com duas casas (o sensor do nRF52833 mede em passos de 0,25 °C) e `vibration_rms` com três.
- **O gateway completa a linha** com `contract.CompleteSerialLine`:
  - Põe o `machine_id` da sua configuração e o `ts` do momento em que recebe a linha, em UTC com milissegundos. Se a linha trouxer esses dois campos, eles são substituídos.
  - Os outros campos passam como vieram, inclusive os desconhecidos, como na bridge.
- **A validação continua na bridge.** O gateway publica a mensagem completa sem validar. Uma leitura fora do contrato, como um sensor fora da faixa, vai para a DLQ com o motivo, como as do simulator.
- **O gateway descarta só o que não consegue completar:**
  - linha que não é um objeto JSON, como a primeira depois de abrir a porta, que pode começar no meio, ou uma mensagem de boot ou de log do Zephyr;
  - linha acima de 256 bytes.
- **Contrato em `contracts/`:**
  - `serial.schema.json` documenta a linha.
  - `contracts/examples/serial/` tem os exemplos, cada um numa linha só, no formato exato que o firmware imprime.
  - Os testes em Go completam cada exemplo e conferem o resultado com o contrato de telemetria. Outro teste confere que o schema da linha tem os campos e limites do schema da telemetria, menos os dois do gateway.

## Alternativas consideradas

- **O firmware mandar a telemetria inteira.** Precisaria de um relógio acertado pela serial e de um `machine_id` gravado em cada placa. O Pi 5 já tem o relógio acertado por NTP e sabe qual placa está em qual porta.
- **CSV ou binário na serial.** Usariam menos bytes, mas 173 bytes por segundo são 1,5% de 115200 bauds. O JSON se lê em qualquer terminal serial e se compara direto com os exemplos.
- **O gateway validar e descartar as leituras inválidas.** O motivo ficaria só no log do Pi 5. Publicando, a leitura inválida aparece na DLQ e na métrica da bridge, junto com as das outras fontes.
- **O gateway montar a mensagem num struct.** Um campo novo do firmware se perderia até alguém mudar o gateway.
- **Modbus RTU já na Fase 4.** É o plano da Fase 5. Começar com uma linha de texto deixa o firmware e o gateway testáveis com um terminal serial e um arquivo.

## Consequências

- Como os exemplos estão no formato exato da linha, o teste do firmware pode comparar a saída do código C com os mesmos arquivos que o Go testa.
- O `ts` é a hora em que a linha chega ao gateway, não a da medida. A diferença é o tempo da serial, poucos milissegundos, e a latência medida pelo pipeline começa no gateway.
- Com o gateway parado, o micro:bit continua mandando linhas que ninguém lê, e elas se perdem. O buffer offline do gateway é da Fase 5.
- Um reinício do micro:bit, pelo botão de reset ou por falta de energia, aparece como um `boot_id` novo, como no simulator (ADR 0003).
- O `machine_id` vem do gateway, então a mesma placa ligada em outro Pi 5 vira outra máquina.
