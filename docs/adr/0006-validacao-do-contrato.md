# 0006. Validação do contrato sem biblioteca de JSON Schema

- Status: aceita
- Data: 2026-11-07

## Contexto

As mensagens são produzidas e consumidas em Go e em Python, e as duas linguagens precisam aceitar e recusar exatamente as mesmas mensagens. O JSON Schema em `contracts/` documenta o formato. Quando uma mensagem é recusada, o motivo vai para a DLQ e precisa apontar o problema com precisão.

## Decisão

- Cada linguagem valida à mão, campo a campo, e o erro sempre começa pelo nome do campo (por exemplo `state: "idle" is not one of [running stopped]`).
- Os exemplos em `contracts/examples/` são compartilhados. Mensagens em `valid/` precisam ser aceitas. Cada caso de `invalid.json` diz qual campo o erro deve apontar, e os testes de Go e de Python conferem isso.
- Um teste em Go lê o JSON Schema e confere se campos obrigatórios, enums, padrões e limites batem com o código.
- Todo campo conhecido é obrigatório, e `null` conta como ausente. Campos desconhecidos são ignorados, para que um produtor possa adicionar um campo antes de os consumidores conhecerem. Mudança incompatível aumenta o `schema_version`.

## Alternativas consideradas

- **Biblioteca de JSON Schema em cada linguagem.** O schema seria a única fonte da validação, mas traria duas dependências novas, mensagens de erro genéricas e mais custo por mensagem.
- **Gerar código a partir do schema.** Evita escrever os tipos, mas adiciona ferramentas ao build e gera código que ninguém lê.
- **Protobuf ou Avro com Schema Registry.** Contrato forte e evolução controlada, mas o Schema Registry está fora do escopo, e um payload binário é mais difícil de inspecionar com `mosquitto_sub` e de produzir no firmware.

## Consequências

- Erros precisos, sem dependência nova e com validação rápida.
- Os exemplos funcionam como documentação executável do contrato.
- A validação existe duas vezes, em Go e em Python. Os exemplos compartilhados reduzem o risco de divergência, mas uma regra nova precisa de um caso novo em `invalid.json`.
- O teste do schema confere só as palavras-chave escolhidas. Uma regra nova no schema precisa entrar no teste também.
- Com campos desconhecidos ignorados, um erro de digitação num campo opcional passaria despercebido. Hoje todos os campos são obrigatórios, então isso não acontece.
