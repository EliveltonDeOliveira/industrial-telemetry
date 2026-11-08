# 0015. Painéis de manutenção no Grafana com usuário só de leitura

- Status: aceita
- Data: 2026-11-08

## Contexto

O ADR 0005 manda o Grafana ler o PostgreSQL direto. O ADR 0010 abriu uma exceção para o OEE, que passa pela API do `telemetry` para a regra de cálculo existir num lugar só. Alertas e ordens não têm regra de cálculo, só dados, e os painéis precisam de contagens por intervalo de tempo, que a API do `maintenance` não oferece. Um painel do Grafana roda SQL livre, então o usuário dele define o que um painel consegue fazer no banco.

## Decisão

- **Fonte dos dados:** o Grafana lê alertas e ordens do PostgreSQL pelo datasource nativo `grafana-postgresql-datasource` (uid `postgres`).
- **Papel `grafana`:**
  - Só tem `SELECT`, também nas tabelas de migrations futuras (`ALTER DEFAULT PRIVILEGES`).
  - Abre transações só de leitura por padrão, então uma escrita falha mesmo se um `GRANT` escapar.
  - Tem `statement_timeout` de 10 segundos, para um painel pesado não segurar o banco.
- **Senha do papel:** vem de `POSTGRES_GRAFANA_PASSWORD`, com padrão `grafana` no compose. O `migrate.sh` passa a senha para a migration `0003_grafana_reader.sql` como variável do psql (`:'grafana_password'`), e assim ela não fica no arquivo versionado.
- **Dashboard "Manutenção"** (uid `maintenance`):
  - Contagem de ordens `aberta` e `em_andamento`, que não depende do período.
  - Alertas no período.
  - Tempo médio até concluir: `closed_at` menos `opened_at` das ordens concluídas no período, sem as canceladas.
  - Alertas por tipo em barras de pelo menos 5 minutos.
  - Tabelas de ordens ativas, alertas do período e ordens fechadas no período.
  - O número da ordem abre o detalhe na API do `maintenance`.
- **Marcas no OEE:** os alertas aparecem como marcas verticais nos gráficos do dashboard "OEE por máquina", filtradas pela mesma variável de máquina. A falha e a detecção ficam no mesmo gráfico.
- Os dois dashboards têm link um para o outro, mantendo o período e as máquinas escolhidas.

## Alternativas consideradas

- **Ler pela API, com o plugin Infinity, como o OEE.** Deixaria um caminho só para os dados de manutenção, mas a API teria de ganhar rotas de agregação só para o Grafana.
- **O mesmo usuário `industrial` dos serviços**, como no ClickHouse. Não precisa de migration, mas um painel conseguiria alterar ou apagar ordens.
- **Senha fixa na migration.** Mais simples, mas seria uma credencial versionada, válida em qualquer ambiente que rodasse as migrations.

## Consequências

- O Grafana só lê. Mudar o estado de uma ordem continua sendo pela API (ADR 0005).
- A senha do papel vale a partir da primeira subida, porque a migration roda uma vez só. Para trocar depois, é preciso `ALTER ROLE grafana PASSWORD ...` e atualizar a variável.
- Uma migration pode receber segredos como variáveis do psql. Cada variável nova entra no `migrate.sh` e no serviço `postgres-migrate` do compose.
- O ClickHouse ainda é lido pelo Grafana com o usuário principal. Um usuário só de leitura lá segue o mesmo raciocínio e fica como evolução.
