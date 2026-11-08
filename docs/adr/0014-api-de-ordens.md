# 0014. API de ordens de manutenção

- Status: aceita
- Data: 2026-11-08

## Contexto

As ordens são abertas pelo `maintenance` a partir dos alertas (ADR 0013) e mudam de estado pelas pessoas, numa API do mini CMMS. Os estados são `aberta`, `em_andamento`, `concluida` e `cancelada`. Uma ordem vai de `aberta` para `em_andamento` e depois para `concluida`, e de `aberta` ou `em_andamento` também pode ir para `cancelada`. Qualquer outra transição é recusada com HTTP 409. Um `PATCH` pode chegar enquanto outro muda a mesma ordem, ou enquanto um alerta entra nela.

## Decisão

- **Rotas:**
  - `GET /work-orders`: lista as ordens, da mais nova para a mais antiga, com filtros opcionais `status` e `machine_id`.
  - `GET /work-orders/{id}`: devolve a ordem com o histórico de eventos e os alertas ligados a ela, do mais antigo para o mais novo. As três consultas rodam numa transação só de leitura, para verem o mesmo instante.
  - `PATCH /work-orders/{id}`: recebe `status`, `assignee` ou os dois, por exemplo `{"status": "em_andamento", "assignee": "tecnico-1"}`, e devolve a ordem como o `GET`.
- **Transições:** a tabela de transições fica num mapa em `services/maintenance/workorder.go`, e a regra é uma função pura testada em tabela.
  - Pedir o estado atual de novo também é 409, porque não é uma transição.
  - Uma ordem `concluida` ou `cancelada` não muda mais, nem o responsável.
- **Histórico:** todo `PATCH` grava um evento em `work_order_events`, também quando só muda o responsável. Nesse caso `from_status` e `to_status` são iguais, e o histórico mostra quem estava com a ordem em cada momento.
- **Concorrência:** o `PATCH` lê a ordem com `SELECT ... FOR UPDATE`, valida a transição, atualiza e grava o evento na mesma transação. Dois `PATCH` na mesma ordem se enfileiram, e o segundo valida contra o estado que o primeiro deixou. O consumidor de alertas faz o mesmo (ADR 0013).
- `closed_at` recebe o `now()` da transação, o mesmo `created_at` do evento que fechou a ordem.
- **Corpo da requisição:**
  - Até 1 KiB, e campos desconhecidos dão 400, como no `POST /faults`.
  - Um campo ausente ou `null` não muda.
  - `assignee` perde os espaços das pontas e fica entre 1 e 100 caracteres.
- **Erros:**
  - 400 para requisição inválida, com o nome do campo na frente, como no contrato.
  - 404 para ordem inexistente e 409 para transição recusada.
  - 500 sem o detalhe, que vai para o log.
- Datas em UTC com milissegundos, como no contrato.
- `writeJSON`, `writeError`, o erro 500 e a leitura do corpo, antes copiados em cada serviço, viram o pacote `internal/httpjson`, usado pelo `simulator`, pelo `telemetry` e pelo `maintenance`.

## Alternativas consideradas

- **Mesmo estado como operação sem efeito, com resposta 200.** Facilita repetir um `PATCH` que deu timeout, mas esconde um cliente com o estado desatualizado: se dois técnicos fecham a mesma ordem, o segundo precisa saber que outro fechou antes.
- **Trocar o responsável de uma ordem fechada.** Reescreveria quem fez o trabalho depois do fato.
- **Uma rota por ação**, como `POST /work-orders/{id}/start`. Deixa cada ação explícita, mas multiplica as rotas. Com `PATCH`, a tabela de transições é a única regra.
- **Concorrência otimista com versão e `If-Match`.** Protege também contra uma troca de responsável feita sobre dados velhos. Para mudança de estado, a tabela de transições já recusa a maior parte desses casos, então fica como evolução.
- **Paginação da lista.** As ordens crescem devagar, porque alertas repetidos entram na ordem ativa. Paginação por `id` fica como evolução.

## Consequências

- Um `PATCH` que funcionou, mas cuja resposta se perdeu, dá 409 se for repetido. O cliente confere o estado com `GET`.
- Duas pessoas mudando o estado da mesma ordem nunca perdem uma mudança: a segunda recebe 409. Duas trocas de responsável ao mesmo tempo são válidas, e vale a última.
- Sem login (fora de escopo), `assignee` é texto livre e não identifica quem fez o `PATCH`.
- Os testes de integração em `services/maintenance/store_test.go` cobrem dois `PATCH` concorrentes: sem o `FOR UPDATE`, o teste falha.
