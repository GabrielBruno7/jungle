# jungle

Serviço de carteira e ledger para provedores de jogos, em Go com Uber Fx. Ele movimenta o saldo dos jogadores a partir das operações que os provedores enviam (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`) e registra cada movimento num ledger append-only.

## Subindo

Precisa de Docker com Compose. Go 1.25.11 (a versão do `go.mod`) só é necessário se você for rodar os testes ou a aplicação fora do container.

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

## Variáveis de ambiente

Os padrões ficam em `internal/config/config.go`, que é a fonte da verdade. O `.env.example` traz os mesmos valores para rodar a aplicação no host contra os serviços do Compose, sem nenhum segredo real.

| Variável | Padrão | Para que serve |
|---|---|---|
| `JUNGLE_PORT` | `8080` | porta do servidor HTTP |
| `JUNGLE_INSTANCE_ID` | `<hostname>-<pid>` | identifica o processo entre as instâncias; fica gravado no claim da outbox, então dá para saber qual instância morreu segurando trabalho |
| `POSTGRES_HOST` / `POSTGRES_PORT` | `localhost` / `5432` | endereço do banco |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | `jungle` | credenciais e banco |
| `POSTGRES_SSLMODE` | `disable` | `sslmode` da connection string |
| `AWS_REGION` | `us-east-1` | região usada pelo SDK |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `test` / `test` | o LocalStack aceita qualquer coisa |
| `SQS_ENDPOINT_URL` | vazio | aponta para o LocalStack em dev; vazio usa a AWS real |
| `SQS_REQUEST_QUEUE_URL` | vazio | fila FIFO de entrada |
| `SQS_EVENT_QUEUE_URL` | vazio | fila FIFO onde a outbox publica |
| `SQS_CONSUMER_NAME` | `wager-transactions-consumer` | escopo da deduplicação na inbox |
| `SQS_MAX_MESSAGES` | `10` | mensagens por `ReceiveMessage` |
| `SQS_WAIT_TIME_SECONDS` | `10` | long polling |
| `SQS_VISIBILITY_TIMEOUT` | `60` | visibility timeout no recebimento |
| `OIDC_ISSUER_URL` | `http://localhost:8081/realms/jungle` | de onde vêm o discovery e as chaves |
| `OIDC_ADDITIONAL_ISSUERS` | vazio | outras grafias aceitas do mesmo `iss`, separadas por vírgula |
| `OIDC_AUDIENCE` | `jungle-api` | claim `aud` exigida |
| `OIDC_DISCOVERY_TIMEOUT` | `90s` | quanto o boot insiste num issuer fora do ar |
| `STARTUP_DEPENDENCY_TIMEOUT` | `60s` | quanto o boot insiste em Postgres ou SQS fora do ar |
| `OUTBOX_INTERVAL` | `1s` | intervalo entre drenagens da outbox |
| `REFERENCE_INTERVAL` | `5s` | intervalo entre tentativas das referências pendentes |
| `SHUTDOWN_TIMEOUT` | `20s` | prazo dos workers para terminar o que está em voo |
| `TRACING_ENABLED` | `true` | `false` instala um tracer no-op e dispensa coletor |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | endpoint OTLP/gRPC (`jaeger:4317` dentro do Compose) |
| `OTEL_SERVICE_NAME` | `jungle` | nome do serviço no Jaeger |
| `TRACING_SAMPLE_RATIO` | `1.0` | sampler por proporção, respeitando decisão do pai |
| `MIGRATIONS_PATH` | `migrations` | diretório lido pelo `cmd/migrate` |

O `OIDC_ADDITIONAL_ISSUERS` existe por um motivo prático: o Keycloak deriva o `iss` do host pelo qual o token foi pedido, então o mesmo realm responde `http://keycloak:8080/realms/jungle` dentro da rede do Compose e `http://localhost:8081/realms/jungle` a partir do host. As duas grafias são listadas explicitamente em vez de desligar a checagem de issuer.

## Migrations

SQL versionado em `migrations/`, aplicado com golang-migrate pelo `cmd/migrate`, que lê as mesmas variáveis de Postgres da aplicação.

```bash
go run ./cmd/migrate up        # aplica tudo que está pendente
go run ./cmd/migrate down      # reverte todas
go run ./cmd/migrate down 1    # reverte só a última
go run ./cmd/migrate version   # versão atual e flag dirty
```

Toda migration tem par `up`/`down`, então dá para ir e voltar. Dentro do Compose isso roda sozinho: o serviço `migrate` aplica as pendentes antes do `app` subir. Para rodar na mão contra o banco do Compose, use `make migrate-up`, `make migrate-down` e `make migrate-version`.

As tabelas criadas são `wallet`, `wager_transaction`, `wallet_ledger_entry`, `journal_entry`, `inbox` e `outbox`, junto com as constraints e triggers que seguram os invariantes financeiros no próprio banco.

## Filas

O `deploy/localstack/init-sqs.sh` roda quando o LocalStack sobe e cria quatro filas FIFO: `wager-transactions.fifo` e `wager-events.fifo`, cada uma com sua DLQ, com `VisibilityTimeout=60` e `maxReceiveCount=5`. Cada fila principal carrega uma access policy em que o provedor enfileira mas não lê, e o serviço lê mas não enfileira. O LocalStack guarda essas policies sem avaliá-las como a AWS de verdade faz.

```bash
make queues    # lista as filas e seus atributos

docker compose exec localstack awslocal sqs receive-message \
  --region us-east-1 \
  --queue-url http://localhost:4566/000000000000/wager-transactions-dlq.fifo
```

## Autenticação

Todo endpoint de negócio exige um bearer token do Keycloak via `client_credentials`. Só os health checks e o `/metrics` são públicos. O realm é importado automaticamente com três clients:

| Client | Secret | Papel | Pode |
|---|---|---|---|
| `jungle-internal` | `internal-secret` | `internal-service` | abrir carteira, ler carteira e ledger, reconciliar, ler transação de qualquer provedor |
| `provider-a` | `provider-a-secret` | `provider` | enviar e ler apenas operações de `provider-a` |
| `provider-b` | `provider-b-secret` | `provider` | enviar e ler apenas operações de `provider-b` |
| `provider-expiring` | `provider-expiring-secret` | `provider` | igual ao `provider-a`, mas com token de 1 segundo; existe só para o teste de credencial expirada |

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

### Sem dependência externa

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -l .        # não imprime nada quando está tudo formatado
```

Ou `make check`, que roda `gofmt -l`, `go vet` (com e sem a tag `integration`) e `go test -race ./...` de uma vez. Esses testes cobrem o domínio (`money`, `wallet`, `wagertx`, `journal`) e o hash de idempotência.

### Integração

A suíte usa Postgres, LocalStack e Keycloak de verdade, nunca mock no lugar de infraestrutura, e fica atrás da tag `integration` para não rodar por acidente. Primeiro prepare as dependências:

```bash
docker compose up -d postgres localstack keycloak
go run ./cmd/migrate up
```

Depois:

```bash
go test -tags=integration -race -count=1 ./internal/integration/...
go test -tags=integration -count=1 ./cmd/jungle/...
```

`make deps` e `make test-integration` fazem exatamente isso, já com as variáveis de ambiente preenchidas. Note o `-count=1`: sem ele o Go devolve resultado em cache e você acha que rodou.

São 47 testes. Os dois cenários obrigatórios de concorrência (duas apostas de 80 sobre saldo de 100, e a mesma aposta enviada 50 vezes em paralelo) rodam contra três instâncias independentes compartilhando o mesmo banco. O resto cobre consumidor interrompido entre o commit e o delete da mensagem, mensagem malformada chegando na DLQ, isolamento entre provedores com tokens reais, credencial expirada recusada, referência resolvida fora de ordem e também rejeitada quando o alvo nunca chega, dois publishers disputando a outbox, republicação preservando o `eventId`, os invariantes do journal e as constraints do banco recusando o que o código não consegue fazer.

Dois testes cobrem especificamente a recuperação: um derruba a instância que processou as operações, sobe outra e confere que idempotência, inbox, saldo e ledger atravessaram o reinício intactos; o outro estaciona um `REFUND` sem referência numa instância, mata ela, e verifica que outra instância liquida a pendência quando a aposta chega. O `cmd/jungle` tem ainda um teste que sobe e derruba a aplicação inteira contra as dependências reais e confere que a porta foi liberada e que nenhuma goroutine vazou.

### Múltiplas instâncias

```bash
docker compose -f docker-compose.yml -f scale.override.yml up -d --scale app=3
```

Detalhes na seção anterior. As três réplicas dividem Postgres e SQS, então é essa a configuração para reproduzir corrida na mesma carteira, publishers competindo e reentrega de mensagem.

### Simulações de falha

Todas assumem a stack no ar e um `WALLET` já aberto.

**Queda no meio do processamento.** Dispare carga e derrube uma réplica sem aviso:

```bash
docker compose -f docker-compose.yml -f scale.override.yml up -d --scale app=3
docker compose kill -s SIGKILL $(docker compose ps -q app | head -1)
```

Reenvie as operações que estavam em voo com a mesma `Idempotency-Key`. Ou elas foram commitadas e voltam como `idempotentReplay: true`, ou nunca existiram e são processadas agora. Meio caminho não acontece, porque saldo, transação, ledger, journal e eventos entram no mesmo commit. Confira com a reconciliação.

**Banco fora do ar.** `docker compose stop postgres` e o `/health/ready` passa a responder `503` dizendo qual dependência caiu, enquanto o `/health/live` continua `200` (uma liveness que morre junto com o banco faz o orquestrador matar um processo saudável). As requisições respondem `503 UNAVAILABLE`, que é retryable, e nada fica pela metade. `docker compose start postgres` e o serviço volta sozinho.

**Mensagem inválida indo para a DLQ.** Mande um corpo quebrado na fila de entrada e veja ele parar na DLQ depois das cinco tentativas, sem nunca virar movimento financeiro:

```bash
docker compose exec -T localstack awslocal sqs send-message \
  --region us-east-1 \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" --message-deduplication-id "broken-1" \
  --message-body 'isto não é json'

make queues   # ApproximateNumberOfMessages na DLQ
```

**Consumidor interrompido depois do commit e antes de apagar a mensagem.** É o caso clássico de reentrega. Parar o container na janela certa na mão é sorte, então ele está automatizado em `TestSQSConsumer_InterruptedAfterCommitBeforeDelete`: o efeito é commitado, a mensagem volta para a fila, e a segunda entrega é barrada pela inbox sem debitar de novo.

**Outbox acumulada.** Pare o app com eventos pendentes, gere movimento e suba de novo:

```bash
docker compose stop app
# ... envie operações pelo SQS ...
docker compose start app
```

Os eventos saem depois, na ordem, com o mesmo `eventId`, e o `jungle_outbox_lag_seconds` volta a zero. É o mesmo comportamento que o teste de carga em `deploy/loadtest` mede sob rajada.

**Referência que nunca chega.** Mande um `REFUND` apontando para uma aposta inexistente. Ele volta `202` com `PENDING_REFERENCE`, o worker tenta com backoff e, depois de 10 tentativas ou 15 minutos, a transação vira `REJECTED` com `REFERENCE_NOT_FOUND`. Para ver isso rápido, suba o app com `REFERENCE_INTERVAL=1s`.

### CI

O `.github/workflows/ci.yml` roda em quatro jobs: checagens estáticas, unitários, a suíte de integração com serviços reais (aplicando, revertendo e reaplicando as migrations), e um job que faz `docker compose up --build` de um clone limpo e dirige um fluxo autenticado de ponta a ponta. Esse último existe para pegar o que o README não pega: se o projeto realmente sobe na mão de outra pessoa.

## Observabilidade

Logs em JSON estruturado. Toda operação liquidada sai numa linha com `correlationId`, `providerId`, `externalTransactionId`, `walletId`, `transactionId`, `kind`, `status` e `source`, o que permite seguir uma operação inteira nos dois caminhos. Token, header de autorização e corpo de requisição nunca são logados.

Métricas Prometheus em `/metrics`, escritas igualmente pelo caminho HTTP e pelo SQS (o label `source` é que separa). As duas que merecem alerta são `jungle_outbox_lag_seconds`, que subindo significa que os consumidores lá fora estão vendo um mundo desatualizado, e `jungle_reconciliation_divergences_total`, que saindo de zero significa estrago real.

O Grafana já vem com o dashboard "Jungle - Wallet & Ledger" provisionado como home, organizado pelas perguntas que se faz em produção e não pela lista de métricas. O Prometheus descobre os targets por DNS do Docker, então `--scale app=3` aparece sozinho.

Tracing com OpenTelemetry exportando para o Jaeger. Um trace cobre a requisição HTTP, cada statement SQL dentro da transação, o span do caso de uso com os atributos da operação e, mais adiante, a publicação do evento e o consumo dele no SQS. Isso último exige um detalhe: quem publica a outbox não é a goroutine que atendeu a requisição, então o contexto vai gravado na coluna `trace_parent` e é restaurado na hora de publicar. Sem isso cada evento abriria um trace solto e a relação de causa se perderia.

Se quiser ver o sistema sob pressão, `deploy/loadtest/run.sh` roda um teste de carga com k6 em três cenários simultâneos e amostra o lag da outbox durante a corrida. Uma execução gravada, com os números e as ressalvas honestas sobre o ambiente, está em [deploy/loadtest/RESULTS.md](deploy/loadtest/RESULTS.md). O resumo é que o gargalo não é o lock de carteira, é o publisher da outbox: ele envia um evento por vez e sustenta uns 300 por segundo, contra as cerca de 900 que o caminho de escrita produz sob carga.

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

