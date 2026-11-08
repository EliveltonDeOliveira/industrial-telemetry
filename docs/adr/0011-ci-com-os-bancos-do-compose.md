# 0011. CI no GitHub Actions com os bancos do compose

- Status: aceita
- Data: 2026-11-08

## Contexto

Os testes rodam hoje só na máquina de desenvolvimento. Os testes de integração do `telemetry` (`services/telemetry/store_test.go`) precisam do ClickHouse e do PostgreSQL com as migrations aplicadas, e são pulados quando `CLICKHOUSE_TEST_DSN` e `POSTGRES_TEST_DSN` não estão definidas. O repositório é público e o CI precisa ficar no plano gratuito do GitHub.

## Decisão

- Workflow em `.github/workflows/ci.yml`, disparado em push na `main` e em pull request.
- Job `unit`: `go vet ./...` e `go test -race ./...`, sem bancos, com os testes de integração pulados.
- Job `integration`, em paralelo: sobe os bancos com `docker compose run` dos serviços `postgres-migrate` e `clickhouse-migrate` do `deploy/docker-compose.yml` e roda `go test -race -count=1 ./...` com os DSNs definidos.
- Runner padrão `ubuntu-latest`, que em repositório público é gratuito e sem limite de minutos (4 vCPUs e 16 GB de RAM).
- O token do workflow só tem leitura, e um push novo na mesma branch cancela a execução anterior.

## Alternativas consideradas

- **Só testes unitários.** Mais simples e rápido, mas o código que grava e lê dos bancos nunca rodaria no CI.
- **Service containers do GitHub Actions.** Recurso nativo, mas as versões das imagens ficariam repetidas no workflow, e as migrations precisariam de um passo à parte, porque esses containers sobem antes do checkout do código.

## Consequências

- O CI usa as mesmas imagens, versões e migrations do desenvolvimento. Mudar a versão de um banco no compose já muda o CI.
- Toda execução aplica as migrations num banco zerado, então uma migration quebrada aparece no CI.
- O job `integration` leva cerca de 30 segundos a mais para subir os bancos, em paralelo com o `unit`.
- `-count=1` desliga o cache de testes do Go no job de integração, porque o cache não enxerga mudanças nos bancos nem nas migrations.
- Os testes unitários rodam nos dois jobs. Em troca, um teste de integração novo, em qualquer pacote, entra no CI sem mudar o workflow.
- `-race` precisa de cgo, que existe no runner. Os binários dos serviços continuam estáticos e sem cgo.
- Na Fase 2, o `pytest` entra como mais um job no mesmo workflow.
