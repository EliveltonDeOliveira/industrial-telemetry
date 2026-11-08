# 0009. Regras de cálculo do OEE

- Status: aceita
- Data: 2026-11-07

## Contexto

A fórmula do OEE já está definida: disponibilidade × performance × qualidade, com o tempo rodando e as peças tirados da telemetria. Só que as leituras são amostras, uma por segundo no simulador. Entre duas leituras pode haver leitura perdida, reinício da máquina ou uma reentrega repetida, e o período pedido raramente começa e termina exatamente numa leitura. A regra precisa dizer o que acontece em cada caso.

## Decisão

- Uma leitura vale até a próxima. O intervalo entre duas leituras conta como rodando quando a primeira está `running`.
- Intervalo entre leituras de `boot_id` diferentes não conta como rodando. A máquina ficou desligada em algum momento dele, sem como saber quanto.
- Peças são a soma do quanto os contadores cresceram entre leituras consecutivas. Se o `boot_id` mudar ou o contador diminuir, o contador recomeçou do zero, e o valor novo inteiro conta como peças. Contadores de `boot_id` diferentes nunca são subtraídos.
- Entram só as leituras dentro do período, e a primeira serve só de base. O tempo antes da primeira leitura e depois da última não conta como rodando.
- Fator sem denominador vale 0: sem tempo rodando, a performance é 0, e sem peças, a qualidade é 0. Cada fator fica limitado entre 0 e 1.
- O cálculo é uma função pura em Go, no pacote `internal/oee`, sem acesso a banco. Quem chama busca as leituras ordenadas por `ts`.

## Alternativas consideradas

- **Descartar intervalos longos entre leituras do mesmo `boot_id`.** Como os contadores são acumulados, as peças feitas durante uma leitura perdida aparecem na leitura seguinte. Descartar o tempo e manter as peças levaria a performance acima de 1.
- **Contar o intervalo do reinício como rodando.** Aumentaria a disponibilidade com tempo em que a máquina estava desligada.
- **Buscar a última leitura antes do período para servir de base.** Mais exato na borda, mas exige outra consulta, e a diferença é de no máximo um intervalo de leitura, 1 segundo no simulador.
- **Qualidade 1 quando não há peças**, por não haver refugo. Mostraria 100% de qualidade numa máquina que não produziu nada.
- **Calcular em SQL no ClickHouse, com funções de janela.** Não traz as leituras para a aplicação, mas é mais difícil de testar em tabela sem um banco e de ler numa revisão.

## Consequências

- Uma leitura repetida soma intervalo zero e nenhuma peça, então o resultado é o mesmo com ou sem `FINAL`.
- Um trecho do período sem leituras, por exemplo com a máquina desligada, derruba a disponibilidade, como uma parada não planejada.
- O cálculo carrega na memória todas as leituras do período: um dia de uma máquina a 1 leitura por segundo são 86.400 leituras. Períodos longos pedem um limite na API ou a leitura do `oee_turno`.
- Outra implementação do OEE, como o ETL em Python da Fase 3, precisa seguir as mesmas regras e passar pelos mesmos casos de teste.
