# jungle

Serviço de carteira e ledger para provedores de jogos, em Go com Uber Fx. Ele movimenta o saldo dos jogadores a partir das operações que os provedores enviam (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`) e registra cada movimento num ledger append-only.

As operações entram por duas portas, a API HTTP e um consumidor SQS FIFO, e as duas executam o mesmo caso de uso. Ou seja: as garantias são iguais nos dois caminhos, não existe "o jeito rápido" e "o jeito certo".

O porquê de cada decisão está em [ARCHITECTURE.md](ARCHITECTURE.md). Este arquivo é só o manual de uso.

## Subindo

Precisa de Docker com Compose. Go 1.25 só é necessário se você for rodar testes ou a aplicação fora do container.

```bash
docker compose up --build
```

Sobe `app`, `postgres`, `localstack` (SQS), `keycloak`, `adminer`, `jaeger`, `prometheus`, `grafana` e um `migrate` que roda as migrations e sai. O `app` só inicia depois que as dependências estão saudáveis e o `migrate` terminou com sucesso, então um `docker compose up` num clone limpo basta.

| Serviço | Endereço | Observação |
|---|---|---|
| app | http://localhost:8080 | API e workers |
| postgres | `localhost:5432` | usuário, senha e banco são `jungle` |
| localstack | http://localhost:4566 | só SQS |
| keycloak | http://localhost:8081 | realm `jungle`, admin `admin`/`admin` |
| adminer | http://localhost:8082 | sistema `PostgreSQL`, servidor `postgres` |
| jaeger | http://localhost:16686 | traces |
| prometheus | http://localhost:9090 | targets em `/targets` |
| grafana | http://localhost:3000 | sem tela de login |

Para conferir se está de pé:

```bash
curl -s http://localhost:8080/health/live
# {"status":"ok"}

curl -s http://localhost:8080/health/ready
# {"checks":{"postgres":"ok","sqs":"ok"},"status":"ok"}
```

`docker compose down -v` derruba tudo e apaga os volumes.

## Autenticação

Todo endpoint de negócio exige um bearer token do Keycloak via `client_credentials`. Só os health checks e o `/metrics` são públicos. O realm é importado automaticamente com três clients:

| Client | Secret | Papel | Pode |
|---|---|---|---|
| `jungle-internal` | `internal-secret` | `internal-service` | abrir carteira, ler carteira e ledger, reconciliar, ler transação de qualquer provedor |
| `provider-a` | `provider-a-secret` | `provider` | enviar e ler apenas operações de `provider-a` |
| `provider-b` | `provider-b-secret` | `provider` | enviar e ler apenas operações de `provider-b` |

Emitir token é o de sempre. Uma função de conveniência, usada nos exemplos abaixo:

```bash
tok() {
  curl -s -X POST http://localhost:8081/realms/jungle/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$2" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'
}

INTERNAL=$(tok jungle-internal internal-secret)
PROVIDER_A=$(tok provider-a provider-a-secret)
```

O provedor autorizado sai do token, nunca do corpo da requisição. Se `provider-a` tentar ler uma transação de `provider-b`, a resposta é `404` e não `403`: um `403` confirmaria que a transação existe.

## Usando

### Abrir uma carteira

```bash
PLAYER=$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL" \
  -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
```

```json
{
  "id": "e5eaa39d-6ab8-4a50-ac79-96c357d72a37",
  "playerId": "b2f982fa-9c23-47e9-bba1-80b0e984a017",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1
}
```

Guarde o id em `WALLET`. Saldo inicial positivo gera também uma transação `OPENING`, o lançamento no ledger e os eventos de integração, tudo no mesmo commit. Saldo inicial zero cria só a linha da carteira, porque não houve movimento nenhum para registrar.

### Enviar uma operação

O header `Idempotency-Key` é obrigatório. O servidor não inventa uma chave quando ela falta.

```bash
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d "{
    \"providerId\": \"provider-a\",
    \"externalTransactionId\": \"transaction-123\",
    \"playerId\": \"$PLAYER\",
    \"walletId\": \"$WALLET\",
    \"roundId\": \"round-987\",
    \"gameId\": \"fortune-chimp\",
    \"kind\": \"BET\",
    \"money\": { \"amount\": \"25.00\", \"currency\": \"BRL\" }
  }"
```

```json
{
  "transactionId": "23311136-e69e-43c5-841a-7b1dfea59025",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

Reenviar a mesma requisição devolve o mesmo corpo com `"idempotentReplay": true` e o saldo **daquele momento**, mesmo que a carteira já tenha andado desde então. É o que um provedor precisa para reconciliar depois de um timeout.

As outras operações têm o mesmo formato. `WIN` credita e aceita `referenceExternalTransactionId` apontando para a aposta da rodada (opcional, informativo). `LOSS` fecha a rodada sem mover dinheiro e exige `"amount": "0.00"`. `REFUND` e `ROLLBACK` exigem a referência, e o segundo estorno da mesma transação é recusado.

### Pelo SQS

Mesmo caso de uso, mesmas garantias. O `MessageGroupId` tem que ser o id da carteira, o que dá ordenação por carteira e paralelismo entre carteiras diferentes. O `MessageDeduplicationId` é obrigatório porque a deduplicação por conteúdo está desligada de propósito: quem garante idempotência é a aplicação, não a janela de 5 minutos do SQS.

```bash
MSG=$(cat <<EOF
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "sqs-transaction-1",
    "idempotencyKey": "provider-a:sqs-transaction-1",
    "playerId": "$PLAYER",
    "walletId": "$WALLET",
    "roundId": "round-990",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "15.00", "currency": "BRL" }
  }
}
EOF
)

docker compose exec -T localstack awslocal sqs send-message \
  --region us-east-1 \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" \
  --message-deduplication-id "msg-123" \
  --message-body "$MSG"
```

Os eventos publicados saem em `wager-events.fifo`. Para espiar sem consumir, use `--visibility-timeout 0` no `receive-message`.

### Conferir a carteira

```bash
curl -s "http://localhost:8080/wallets/$WALLET" -H "Authorization: Bearer $INTERNAL"
curl -s "http://localhost:8080/wallets/$WALLET/ledger?limit=20" -H "Authorization: Bearer $INTERNAL"
curl -s -X POST "http://localhost:8080/wallets/$WALLET/reconciliation" -H "Authorization: Bearer $INTERNAL"
```

A reconciliação recalcula o saldo a partir do ledger e compara com o saldo armazenado, num snapshot consistente. Ela nunca escreve. `consistent: false` significa que aconteceu algo que deveria ser impossível.

## Referência rápida

| Método | Rota | Auth |
|---|---|---|
| `GET` | `/health/live`, `/health/ready`, `/metrics` | pública |
| `POST` | `/wallets` | interno |
| `GET` | `/wallets/:walletId` | interno |
| `GET` | `/wallets/:walletId/ledger` | interno |
| `POST` | `/wallets/:walletId/reconciliation` | interno |
| `POST` | `/wagering/transactions` | provedor |
| `GET` | `/wagering/transactions/:transactionId` | provedor |
| `GET` | `/providers/:providerId/wagering/transactions/:externalTransactionId` | provedor |

O ledger é paginado por cursor opaco (`?cursor=&limit=`, padrão 50, máximo 200). O header `X-Correlation-Id` é respeitado quando vem, gerado quando não vem, e aparece nos logs e nos eventos.

Resultado da operação para status HTTP:

| Status da transação | HTTP |
|---|---|
| `PROCESSED` | `200` (`201` ao abrir carteira) |
| `REJECTED` | `422`, com `failureCode` no corpo |
| `PENDING_REFERENCE` | `202` |

Erros vêm como `{"code": "...", "message": "..."}`:

| HTTP | `code` | Quando |
|---|---|---|
| `400` | `INVALID_REQUEST` | corpo inválido, valor monetário malformado, `Idempotency-Key` faltando |
| `401` | `UNAUTHENTICATED` | credencial ausente, inválida ou expirada |
| `403` | `FORBIDDEN` | autenticado mas sem permissão para a rota |
| `404` | `NOT_FOUND` | não existe, ou é de outro provedor |
| `409` | `WALLET_EXISTS` | já existe carteira para esse jogador e moeda |
| `409` | `IDEMPOTENCY_CONFLICT` | mesma chave com payload diferente |
| `409` | `OPERATION_CONFLICT` | mesma operação reenviada sob outra chave |
| `503` | `UNAVAILABLE` | falha transitória de dependência ou corrida perdida; pode repetir |

## O que sustenta a corretude

Esta é a parte que interessa num serviço financeiro, então vale listar o que está de fato implementado.

**Dinheiro é `int64` em centavos.** Nenhum `float` em nenhum ponto: nem no parsing, nem no cálculo, nem no JSON, nem no banco. O `Money` é imutável, carrega a moeda junto, recusa escala diferente de 2, notação científica e operação entre moedas diferentes, e verifica overflow em toda soma.

**Um commit por operação.** Saldo novo, linha da transação, lançamento do ledger, par do journal, linha da inbox e os eventos da outbox entram ou não entram juntos. A fronteira transacional é o `UnitOfWork.Within`, nunca um repositório.

**Idempotência em três camadas.** A `Idempotency-Key`, a identidade de negócio `(providerId, externalTransactionId)` e a inbox no caminho SQS. As três são únicas no banco, então nem trocando a chave dá para processar a mesma operação duas vezes. O hash canônico do payload é o mesmo nos dois caminhos, o que faz HTTP e SQS concordarem sobre o que é "a mesma operação". Nada disso vive em memória: reiniciar todos os processos não perde nada.

**Concorrência sem lock global.** Cada operação trava a linha da carteira com `SELECT ... FOR UPDATE` antes das checagens de duplicidade, e o `UPDATE` ainda compara e troca a `version`. Carteiras diferentes seguem em paralelo. O teste obrigatório do desafio (carteira com 100, duas apostas de 80 simultâneas) roda contra três instâncias independentes: uma processa, a outra é rejeitada por saldo, e o saldo final é 20.

**Eventos só depois do commit.** Outbox transacional, com claim por `FOR UPDATE SKIP LOCKED` e `locked_until`, então vários publishers não duplicam envio. Uma republicação preserva o `eventId`, que é a chave de deduplicação do consumidor lá na frente.

**O banco impede o que o código não deve fazer.** Saldo negativo, ledger editado ou apagado, transação terminal reaberta, duas aberturas para a mesma carteira, referência obrigatória ausente: tudo isso é constraint ou trigger, não convenção. Vale a pena ler o §9 do ARCHITECTURE.

**Partidas dobradas.** Além do ledger por carteira, cada movimento posta um par DEBIT/CREDIT em contas derivadas (`WALLET:<id>`, `PROVIDER:<id>`, `PLATFORM_FUNDING`). Assim a soma de todas as contas é zero e dinheiro surgindo do nada vira erro de aritmética, detectável. Quem garante o balanceamento é uma constraint trigger deferida, que checa no `COMMIT`, porque "somar zero" não é propriedade de uma linha sozinha.

**Referência que chega atrasada não quebra nada.** Um `REFUND` cuja aposta ainda não chegou fica `PENDING_REFERENCE` e um worker tenta de novo com backoff. Depois de 10 tentativas ou 15 minutos, vira `REJECTED` com `REFERENCE_NOT_FOUND`.

## Rodando várias instâncias

O Compose publica a porta 8080 fixa, então uma segunda réplica colidiria. O `scale.override.yml` troca isso por uma faixa:

```bash
docker compose -f docker-compose.yml -f scale.override.yml up -d --scale app=3
```

Cada réplica pega uma porta (8090, 8091, 8092) e todas dividem Postgres e SQS. É essa configuração que os cenários distribuídos exercitam: apostas concorrentes na mesma carteira, publishers competindo pela outbox, consumidor interrompido no meio.

## Testes

```bash
make check             # gofmt, vet e testes unitários com -race
make deps              # só as dependências, para rodar testes no host
make migrate-up
make test-integration  # a suíte contra serviços reais
```

Os unitários cobrem o domínio (`money`, `wallet`, `wagertx`, `journal`) e o hash de idempotência, sem depender de nada externo.

A suíte de integração são 42 testes atrás da tag `integration`, rodando contra Postgres, LocalStack e Keycloak de verdade. Nenhum mock no lugar de infraestrutura. Ela cobre os dois cenários obrigatórios de concorrência, o consumidor interrompido entre o commit e o delete da mensagem, mensagem malformada indo para a DLQ, isolamento entre provedores com tokens reais, referência resolvida fora de ordem, publishers competindo, republicação preservando o `eventId`, os invariantes do journal e as constraints do banco recusando o que o código não consegue fazer. Tem também um teste que sobe e derruba a aplicação inteira e confere que a porta foi liberada e que nenhuma goroutine vazou.

O CI (`.github/workflows/ci.yml`) roda em quatro jobs: checagens estáticas, unitários, a suíte de integração com serviços reais (aplicando, revertendo e reaplicando as migrations), e um job que faz `docker compose up --build` de um clone limpo e dirige um fluxo autenticado de ponta a ponta. Esse último existe para pegar o que o README não pega: se o projeto realmente sobe na mão de outra pessoa.

## Observabilidade

Logs em JSON estruturado. Toda operação liquidada sai numa linha com `correlationId`, `providerId`, `externalTransactionId`, `walletId`, `transactionId`, `kind`, `status` e `source`, o que permite seguir uma operação inteira nos dois caminhos. Token, header de autorização e corpo de requisição nunca são logados.

Métricas Prometheus em `/metrics`, escritas igualmente pelo caminho HTTP e pelo SQS (o label `source` é que separa). As duas que merecem alerta são `jungle_outbox_lag_seconds`, que subindo significa que os consumidores lá fora estão vendo um mundo desatualizado, e `jungle_reconciliation_divergences_total`, que saindo de zero significa estrago real.

O Grafana já vem com o dashboard "Jungle - Wallet & Ledger" provisionado como home, organizado pelas perguntas que se faz em produção e não pela lista de métricas. O Prometheus descobre os targets por DNS do Docker, então `--scale app=3` aparece sozinho.

Tracing com OpenTelemetry exportando para o Jaeger. Um trace cobre a requisição HTTP, cada statement SQL dentro da transação, o span do caso de uso com os atributos da operação e, mais adiante, a publicação do evento e o consumo dele no SQS. Isso último exige um detalhe: quem publica a outbox não é a goroutine que atendeu a requisição, então o contexto vai gravado na coluna `trace_parent` e é restaurado na hora de publicar. Sem isso cada evento abriria um trace solto e a relação de causa se perderia.

Se quiser ver o sistema sob pressão, `deploy/loadtest/run.sh` roda um teste de carga com k6 em três cenários simultâneos e amostra o lag da outbox durante a corrida. Uma execução gravada, com os números e as ressalvas honestas sobre o ambiente, está em [deploy/loadtest/RESULTS.md](deploy/loadtest/RESULTS.md). O resumo é que o gargalo não é o lock de carteira, é o publisher da outbox, que envia um evento por vez.

## Configuração

Os defaults estão em `internal/config/config.go`, que é a fonte da verdade. O `.env.example` espelha os valores para rodar a aplicação no host contra os serviços do Compose. As variáveis são as óbvias (`JUNGLE_PORT`, `POSTGRES_*`, `SQS_*`, `OIDC_*`, `OUTBOX_INTERVAL`, `REFERENCE_INTERVAL`, `SHUTDOWN_TIMEOUT`, `TRACING_*`, `OTEL_*`), mas três merecem nota:

- `JUNGLE_INSTANCE_ID` (padrão `<hostname>-<pid>`) identifica o processo entre as instâncias e fica gravado no claim da outbox, então dá para saber qual instância morreu segurando trabalho.
- `OIDC_ADDITIONAL_ISSUERS` existe porque o Keycloak deriva o `iss` do host pelo qual o token foi pedido: o mesmo realm responde `http://keycloak:8080/realms/jungle` dentro da rede do Compose e `http://localhost:8081/realms/jungle` do host. As duas grafias são listadas explicitamente em vez de desligar a checagem de issuer.
- `TRACING_ENABLED=false` instala um tracer no-op, e aí o serviço roda normalmente sem nenhum coletor por perto.

Migrations ficam em `migrations/`, são aplicadas com golang-migrate pelo `cmd/migrate` (`up`, `down [n]`, `version`) e usam as mesmas variáveis de Postgres da aplicação.

As quatro filas FIFO (`wager-transactions.fifo` e `wager-events.fifo`, cada uma com sua DLQ) são provisionadas pelo `deploy/localstack/init-sqs.sh` com `maxReceiveCount=5`. Cada uma carrega ainda uma access policy: o provedor enfileira mas não lê, o serviço lê mas não enfileira. O LocalStack guarda essas policies sem avaliá-las como a AWS de verdade faz.

## Estrutura

```
cmd/jungle/        composição dos módulos Fx
cmd/migrate/       CLI de migrations

internal/
  domain/          Go puro, sem Fx, HTTP, SQL ou SQS
    money/         valor monetário imutável em centavos
    wallet/        agregado Wallet e o lançamento do ledger
    wagertx/       transação, tipos, estados e códigos de falha
    journal/       contas, movimentos e balanceamento das partidas dobradas
    event/         envelope dos eventos de integração
  app/             casos de uso, ports e a fronteira transacional
  postgres/        adaptador pgx: repositórios e unit of work
  queue/           adaptador SQS: consumidor FIFO e publisher da outbox
  httpapi/         adaptador Gin: handlers, DTOs e autenticação OIDC
  observability/   métricas, /metrics e tracing
  integration/     suíte de integração (tag `integration`)
  health/ router/ server/ config/ worker/

migrations/        SQL versionado (up/down)
deploy/            keycloak, localstack, prometheus, grafana, loadtest, postman
docs/              o enunciado do desafio
```

As limitações conhecidas e o que ficou de fora estão no §12 do ARCHITECTURE.md, listados sem maquiagem.
