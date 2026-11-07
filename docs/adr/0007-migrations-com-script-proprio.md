# 0007. Migrations com script próprio e CLIs oficiais

- Status: aceita
- Data: 2026-11-07

## Contexto

O schema do ClickHouse e do PostgreSQL vai mudar ao longo das fases. As mudanças precisam ser aplicadas na subida do compose, sem apagar os volumes, e o projeto evita dependências que não sejam necessárias.

## Decisão

- Arquivos SQL numerados em `deploy/migrations/{postgres,clickhouse}/NNNN_nome.sql`.
- O `deploy/migrations/migrate.sh`, com menos de 40 linhas, aplica cada arquivo uma vez, em ordem, e registra a versão em `schema_migrations` no próprio banco. Ele usa o `psql` e o `clickhouse-client` das mesmas imagens que já estão no compose.
- Ele roda nos serviços `postgres-migrate` e `clickhouse-migrate`, que sobem depois que o banco fica saudável e encerram ao terminar.
- No PostgreSQL, o arquivo e o seu registro rodam numa transação só. No ClickHouse não há DDL transacional.
- Uma migration aplicada nunca é editada. Mudança vira arquivo novo.

## Alternativas consideradas

- **Pasta de inicialização das imagens (`/docker-entrypoint-initdb.d`).** Mais simples, mas só roda com o volume vazio. Uma migration nova exigiria apagar os dados.
- **golang-migrate, goose ou dbmate.** Ferramentas maduras, com trava contra execução concorrente, migrations de volta e checagem de alteração. Trariam uma dependência nova, e o suporte a ClickHouse é mais limitado que o de PostgreSQL.
- **Migrador em Go no próprio repositório.** Mais controle, mas mais código para manter do que o problema pede.

## Consequências

- Nenhuma dependência nova, e o mecanismo inteiro cabe numa leitura.
- Uma migration com erro no PostgreSQL não deixa nada para trás. No ClickHouse, uma falha no meio de um arquivo precisa ser corrigida à mão.
- Não há trava contra duas execuções ao mesmo tempo. No compose roda um migrador por banco, então isso não acontece.
- Não há migration de volta: desfazer é escrever uma migration nova.
- Não há checagem de alteração: editar um arquivo já aplicado passa despercebido, por isso a regra de nunca editar.
- Se o projeto crescer para vários ambientes, vale trocar o script por uma ferramenta como golang-migrate.
