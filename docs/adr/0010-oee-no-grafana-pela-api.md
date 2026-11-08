# 0010. OEE no Grafana pela API do telemetry

- Status: aceita
- Data: 2026-11-07
- Complementa o ADR 0005.

## Contexto

O ADR 0005 liga o Grafana direto ao ClickHouse, ao PostgreSQL e ao Prometheus. O OEE, porém, é calculado em Go, no pacote `internal/oee`, com as regras do ADR 0009, e sai pela API do `telemetry` em `GET /machines/{id}/oee`. Para mostrar o OEE, o dashboard precisa repetir a regra em SQL ou ler o resultado já calculado.

## Decisão

- O Grafana lê o OEE da API do `telemetry` pelo plugin Infinity (`yesoreyeram-infinity-datasource`), passando o período do dashboard em `from` e `to`.
- Estado, temperatura e vibração ao longo do tempo continuam vindo direto do ClickHouse, pelo plugin oficial.
- A lista de máquinas do dashboard vem de `GET /machines`, só com as que já mandaram alguma leitura.
- Os plugins têm versão fixada no compose e são instalados antes de o Grafana subir, para o provisionamento encontrá-los.

## Alternativas consideradas

- **OEE em SQL no ClickHouse**, com funções de janela. Segue o ADR 0005 sem mudança, mas a regra passaria a existir em dois lugares, com risco de divergir. O ciclo ideal, que está no PostgreSQL, também teria de chegar ao ClickHouse.
- **OEE como métrica no Prometheus**, publicado pelo `telemetry` em `/metrics`. Simples e com a regra num lugar só, mas o período fica fixo e mistura indicador de negócio com métrica de plataforma.

## Consequências

- Uma única implementação do OEE alimenta a API e o dashboard, e o dashboard demonstra a API.
- Cada máquina faz três chamadas à API a cada atualização (OEE, fatores e peças), cada uma lendo as leituras do período.
- Com o `telemetry` fora do ar, os painéis de OEE mostram erro, e os de leituras continuam funcionando.
- Período acima de 7 dias é recusado pela API, e o painel mostra só o código 400, sem a mensagem.
- O dashboard mostra o OEE do período escolhido, não a evolução dele. A evolução por turno virá do `oee_turno`, na Fase 3.
- Mais um plugin é baixado na primeira subida, que já precisava de internet por causa do plugin do ClickHouse.
