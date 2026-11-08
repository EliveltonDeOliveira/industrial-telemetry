# 0005. Grafana no lugar de frontend próprio

- Status: aceita
- Data: 2026-11-07
- Complementado pelo [ADR 0010](0010-oee-no-grafana-pela-api.md): o OEE vem da API do `telemetry`.

## Contexto

O projeto mostra backend e dados. Ainda assim precisa de telas: OEE por máquina, alertas, ordens de serviço e a saúde da própria plataforma. Um frontend próprio custaria tempo e não mostraria as habilidades que interessam.

## Decisão

- Toda visualização fica no Grafana, com datasources e dashboards provisionados como código em `deploy/grafana/` e versionados no repositório.
- O Grafana lê o ClickHouse (pelo plugin oficial), o PostgreSQL e o Prometheus.
- Ações, como mudar o estado de uma ordem, são feitas pela API REST do `maintenance`, não pelo Grafana.

## Alternativas consideradas

- **SPA própria, em React ou similar.** Controle total do visual, mas muito código fora do foco do projeto.
- **Metabase ou Superset.** Bons para análise ad hoc, mas mais fracos em séries temporais ao vivo e em métricas de plataforma.

## Consequências

- Nenhuma linha de frontend, e os dashboards sobem prontos com o `docker compose up`.
- Uma ferramenta só para o negócio (OEE, alertas, ordens) e para a plataforma (vazão, lag, latência, erros), como é comum em times de operação.
- Não há tela para operar as ordens. A demonstração usa `curl`, o que basta para o objetivo do projeto.
- O plugin do ClickHouse é baixado quando o Grafana sobe, então a primeira subida precisa de internet.
- O JSON de dashboard é verboso e difícil de revisar em diff.
