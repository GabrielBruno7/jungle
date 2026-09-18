# Architecture

This document records the technical decisions behind `jungle` and, more
importantly, the reasoning for each one. It is written to be cross-checked
against the code: every claim below corresponds to something in the
repository, and the relevant file is named wherever it helps.

The system processes financial operations from game providers against player
wallets, through two entry points (HTTP and SQS) that must offer *equivalent*
guarantees, across multiple instances, under at-least-once delivery and
arbitrary crashes.

## Layering

```
cmd/jungle            composition root (Fx)
internal/domain/…     money, wallet, wagertx, event — plain Go, no infra
internal/app/…        use cases + port interfaces (no pgx, Gin, SQS)
internal/postgres/…   repository + unit-of-work adapters
internal/httpapi/…    HTTP adapter (Gin) + OIDC authentication
internal/queue/…      SQS consumer + outbox publisher adapters
internal/worker/…     background loops bound to the Fx lifecycle
```

Dependencies point inward. `internal/domain` imports nothing but the standard
library plus `github.com/google/uuid`; `internal/app` imports the domain and
its own port interfaces. The adapters implement those ports, and
`internal/postgres/repos.go` carries compile-time assertions
(`var _ app.Repositories = (*Repos)(nil)`) so any drift between a port and its
adapter fails the build rather than the wiring.

---

## 1. Money

### Representation

`money.Money` (`internal/domain/money/money.go`) is an immutable value object
holding an `int64` count of **minor units** (cents) plus a `Currency`. Both
fields are unexported; the only ways in are `Parse`, `ParseNonNegative`,
`Zero`, and `FromMinorUnits`.

Floating point is never involved at any stage — parsing, arithmetic,
serialization, or persistence. Binary floating point cannot represent most
decimal fractions exactly, so errors accumulate across a long series of
movements and produce balances that disagree with the ledger by amounts no
transaction explains. Integer arithmetic on minor units is exact by
construction, which is the property a ledger needs.

**Why `int64` rather than a decimal library.** A decimal library
(`shopspring/decimal` and friends) buys arbitrary precision and variable
scale. Neither is needed here: the contract fixes the scale at two decimal
places, and `int64` covers ±92,233,720,368,547,758.07 in minor units — far
beyond any realistic wallet. In exchange, `int64` avoids a dependency, keeps
arithmetic to single CPU instructions, and maps to `BIGINT` with no
conversion or rounding step where precision could quietly leak. The cost is
that overflow must be handled explicitly, which is done below.

### Overflow

Every operation that can leave the `int64` range returns `ErrOverflow`
instead of wrapping. The checked helpers live at the bottom of `money.go`:

| Operation | Check |
|---|---|
| `Add` | `addInt64` detects sign-inconsistent results |
| `Sub` | `subInt64` = `addInt64(a, -b)`, with the negation checked first |
| `Negate` | `negateInt64` rejects `math.MinInt64` (no positive counterpart) |
| `Parse` | `strconv.ParseInt` on the integer part, then a checked `mulInt64(·, 100)` and a checked `addInt64` for the fraction |

### Parsing

`Parse` accepts exactly one shape, enforced by an anchored regular
expression: `^(-)?([0-9]+)\.([0-9]{2})$`. Because the pattern is anchored and
matches no letters, it rejects the empty string, `NaN`, `Infinity`,
scientific notation (`1e10`), a missing or extra decimal digit, a comma
separator, thousands separators, leading/trailing whitespace, and an explicit
`+` — all by construction, with no per-case special handling.

**Nothing is silently normalized.** An input that is not exactly
`[-]digits.dd` is rejected outright rather than rounded or reinterpreted.
This matters for idempotency: if the parser quietly rewrote `25.000` to
`25.00`, two inputs that a provider considers distinct would collapse to the
same payload hash. The one normalization that does occur is `-0.00` → `0.00`,
because a negative zero has no meaning and `IsNegative()` must not report
true for it.

`Parse` permits negative values because internal differences legitimately are
negative — the reconciliation `difference` field, for instance.
`ParseNonNegative` wraps it and rejects negatives, and **every external
financial input goes through `ParseNonNegative`** (`internal/httpapi/dto.go`,
`internal/queue/consumer.go`).

### Persistence mapping

| Domain | Column |
|---|---|
| `Money.MinorUnits()` | `BIGINT` (`*_minor_units`) |
| `Money.Currency()` | `CHAR(3)` with a `~ '^[A-Z]{3}$'` check |

Reading back uses `money.FromMinorUnits`, which performs no parsing: the
value was already validated on the way in, and re-parsing a decimal string
would be an opportunity to lose information rather than a safety net.

`Currency` validates format only (three uppercase letters); it does not carry
the ISO 4217 registry. A bare conversion `money.Currency("xx")` bypasses
validation and is treated as a programming error, not a domain error.

---

## 2. Transaction boundary

The SQL transaction is delimited **at the use case**, by
`app.UnitOfWork.Within` (implemented in `internal/postgres/repos.go`). No
repository opens, commits, or rolls back anything; each one receives a
`Querier`, which both `*pgxpool.Pool` and `pgx.Tx` satisfy, so the same
repository code serves a pooled read and a transactional write without
knowing which it is running against.

`Within` begins a transaction, hands transaction-bound repositories to the
callback, and commits only if the callback returns `nil`. The rollback runs
on `context.WithoutCancel(ctx)` — a cancelled request context must not
prevent the rollback from reaching the server, or the connection would be
returned to the pool still holding locks.

For one external operation, a single commit covers:

- the wallet's new balance and version,
- the `wager_transaction` row (terminal, or `PENDING_REFERENCE`),
- the `wallet_ledger_entry` (when money actually moved),
- the balanced pair of `journal_entry` rows for the same movement,
- the `inbox` row (SQS input only),
- every `outbox` event the operation produced.

A rejection commits too. A business rejection is a *result* the provider can
read back and replay — not a discarded request — so the transaction row and
its `WagerTransactionRejected` event are persisted, while the wallet is left
untouched and no ledger entry is written.

### Ordering inside the transaction

Ordering within a transaction is normally irrelevant, since everything lands
in one commit. One case is not: `wallet_ledger_entry.transaction_id` is a
foreign key to `wager_transaction(id)`, and PostgreSQL checks foreign keys at
**statement** time, not at commit. Writing the ledger entry before the
transaction row fails immediately with SQLSTATE 23503.

This was found by running the stack, not by reading the code. `settle` in
`internal/app/process_wager.go` therefore marks the transaction processed and
saves it *first*, then updates the wallet balance, then appends the ledger
entry. The comment at that spot records the reason so the order is not
"tidied" back later.

### The double-entry journal

`wallet_ledger_entry` is single-entry: it answers "what happened to this
wallet, and what was the balance before and after". That is what the wallet
endpoints read and what reconciliation recomputes, and the challenge lists
it as sufficient.

Alongside it, every movement also posts a balanced pair to `journal_entry`
(`internal/domain/journal`): a `DEBIT` on one account and a `CREDIT` of the
same amount on another, in the same commit as everything else. The accounts
are synthetic and derived, not rows in a table — `WALLET:<id>`,
`PROVIDER:<id>`, and `PLATFORM_FUNDING` for the initial credit that opens a
funded wallet. A BET debits the wallet and credits the provider; a WIN,
REFUND or ROLLBACK moves it the other way.

Two things make this worth the extra write. First, money now has a
counterparty: single-entry says a wallet lost `80.00`, double-entry says the
provider gained it, so the sum over *every* account is zero and a whole
class of "money appeared from nowhere" bugs becomes arithmetically
detectable (`GlobalImbalance`, asserted in
`internal/integration/journal_test.go`). Second, the invariant is enforced
by a deferred constraint trigger rather than by the code that writes it
(§9), so an unbalanced pair cannot be committed even by a future code path
that forgets `AssertBalanced`.

A LOSS posts nothing, because no money moves: it is a zero-amount
acknowledgement, and a zero-amount movement is not a movement. A rejected
operation posts nothing either.

---

## 3. Idempotency

Three independent layers, deliberately overlapping.

### Layer 1 — the Idempotency-Key

`wager_transaction.idempotency_key` carries a `UNIQUE` constraint. The key is
whatever the client sent in the `Idempotency-Key` header (or
`data.idempotencyKey` for SQS); the server never substitutes a computed key
for a missing or different one — a request without the header is rejected
with `400 INVALID_REQUEST`.

### Layer 2 — the operation's business identity

`UNIQUE (provider_id, external_transaction_id)`. This is what stops the same
financial operation from being reapplied under a *different* key: the use
case looks the pair up, and if the stored row carries a different
idempotency key it returns `ErrOperationIdentityConflict` → `409`.

The two constraints are independent on purpose. The first is the literal
mechanism the HTTP contract specifies; the second is the invariant that
actually protects the money, and it holds even if a client generates a fresh
key for every retry.

### Layer 3 — the inbox (SQS only)

`inbox` is keyed by `(consumer_name, message_id)`. A redelivery of a message
this consumer already handled finds the row, loads the linked transaction,
and returns its stored result without touching the wallet. The key is scoped
per consumer so a second logical consumer of the same queue is not
short-circuited by the first one's work.

The row also stores the payload hash, and a redelivery is only served from
the inbox when its hash matches. A message id that comes back carrying
different business fields is not the same message, so it is refused as an
idempotency conflict (permanent, therefore DLQ) rather than silently
answered with the first message's result. SQS will not rewrite a body on its
own, but the message id is supplied by the producer, and trusting it alone
would let a wrong reuse of an id return someone else's outcome.

### The payload hash

`app.PayloadHash` (`internal/app/idempotency.go`) distinguishes a legitimate
replay from a conflict. The algorithm:

1. **Fields included** (ten): `providerId`, `externalTransactionId`,
   `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`,
   `money.currency`, `referenceExternalTransactionId`.
2. **Fields excluded**: the Idempotency-Key itself, and every transport
   detail — HTTP headers, the SQS envelope, receipt handles, timestamps,
   correlation ids.
3. **Canonical form**: the fields are placed in a `map[string]string` and
   marshalled with `encoding/json`, which emits map keys in sorted order.
   That sorted-key JSON *is* the canonical form; no separate canonicalizer
   is needed.
4. **Normalization**: `money.amount` is the value's `DecimalString()`
   (`"25.00"`). No other normalization exists, because the parser rejects
   every other spelling rather than rewriting it.
5. **Digest**: SHA-256, hex-encoded.

Because the excluded set is exactly the transport, the HTTP handler
(`buildProcessCommand`) and the SQS consumer (`buildCommand`) compute the
**same hash for the same operation**. That is what makes "the same bet
arriving over both doors" a replay rather than a double spend.

Same key + same hash → the stored result, with `idempotentReplay: true`.
Same key + different hash → `409 IDEMPOTENCY_CONFLICT`.

### Replays return the original balance

`wager_transaction.resulting_balance_minor_units` stores the wallet balance
observed **at the moment the transaction was processed**, and a schema check
enforces that it is set if and only if the status is `PROCESSED`. A replay
returns that snapshot, not the wallet's current balance — so a provider
retrying an old bet after ten other operations still sees the number it
originally saw, which is what makes the response safely cacheable and
comparable on their side.

### Fast path

`fastReplay` answers an already-settled repeat without opening a write
transaction or locking the wallet. It only short-circuits when the stored
transaction is **terminal** and the input is not an SQS delivery (which still
needs its inbox row written). Anything in flight falls through to the locked
path — the fast path is an optimization and never makes a decision.

---

## 4. Concurrency

### The chosen strategy

Coordination is **per wallet**, using two mechanisms together:

1. **Pessimistic row lock (primary).**
   `SELECT … FROM wallet WHERE id = $1 FOR UPDATE` in
   `walletRepo.GetForUpdate`. Writers to the same wallet queue behind each
   other for the duration of the transaction.
2. **Version compare-and-swap (second barrier).**
   `UPDATE wallet SET balance = …, version = $2 WHERE id = $4 AND version = $5`.
   Zero rows affected means another writer moved the row first, reported as
   `ErrConcurrencyConflict` rather than silently overwriting.

**Why both.** The lock alone is sufficient in the current code paths, and the
CAS alone would also be correct but wasteful — every loser would burn a full
attempt and have to retry. Together, the lock does the actual serialization
(no wasted work, and the insufficient-balance decision is made against the
true current balance), while the CAS is a cheap assertion that catches the
case the lock cannot: a future code path that forgets to lock. A lost update
then becomes a loud error instead of missing money.

### Why the lock comes before the de-duplication checks

This ordering is load-bearing and was chosen deliberately (see the comment in
`ProcessWager.Execute`). pgx runs at READ COMMITTED, where each *statement*
takes a fresh snapshot. Acquiring the wallet's row lock first means that by
the time the lock is held, every earlier transaction on that wallet has
committed and is therefore visible to the subsequent lookups.

The reverse order breaks: two identical requests both run their dedup lookups
before either commits, both see "nothing recorded", both proceed, and one of
them only discovers the collision when it hits the unique constraint —
turning a clean replay into an error.

### No global lock

The lock is on one row of `wallet`. Two operations on different wallets never
contend: they lock different rows, run concurrently, and commit
independently. There is no table lock, no advisory lock, and no leader
election anywhere in the system. The background workers use
`FOR UPDATE SKIP LOCKED` for the same reason — several instances can run the
same worker and simply take disjoint rows.

Should the CAS ever fail, the caller is told over HTTP with
`503 UNAVAILABLE` and the message "concurrent modification, retry the
request": the operation had no effect, so retrying is both safe and correct.

### Verified behaviour

Both mandatory scenarios were exercised against the running stack (Postgres,
LocalStack, and Keycloak in containers), driving the real HTTP API with real
tokens:

| Scenario | Result |
|---|---|
| Wallet with `100.00`; two concurrent `80.00` BETs under distinct keys | One `PROCESSED` (`200`), one `REJECTED` with `INSUFFICIENT_BALANCE` (`422`); final balance `20.00`; wallet version `2`; exactly **one** debit in the ledger |
| The same BET sent **50 times in parallel** under one key | **One** distinct `transactionId`, no errors; one debit; balance moved exactly once; wallet version `2` |
| Operations on different wallets | Processed concurrently, no contention |

---

## 5. Pending references

A REFUND or ROLLBACK can legitimately arrive before the operation it
reverses. Rejecting it would be wrong (the target may be milliseconds away);
blocking on it would hold a wallet lock indefinitely. Instead the transaction
is persisted as `PENDING_REFERENCE`, a `WagerTransactionPendingReference`
event is emitted, the HTTP caller gets `202 Accepted`, and — for SQS — the
inbox row is written so the message can be deleted. Continuation becomes the
reference worker's job, which is what makes it survive a restart of every
process.

### Retry policy

`DefaultReferencePolicy` (`internal/app/resolve_references.go`):

| Setting | Value |
|---|---|
| Base backoff | 2s |
| Growth | exponential (`2^(attempt-1) × base`) |
| Max backoff | 1 minute |
| Max attempts | 10 |
| TTL | 15 minutes |

Retry state lives in the table (`reference_attempts`,
`reference_first_seen_at`, `reference_next_attempt_at`, defaulting to `now()`
so a freshly parked row is immediately due), not in memory — so any instance
can pick up the work and a restart loses nothing. The worker claims due rows
with `FOR UPDATE SKIP LOCKED`, and reloads each transaction inside its own
transaction before acting, because another instance may have settled it
between the claim and the attempt.

Whichever budget is exhausted first — attempts **or** TTL — ends the wait:
the reversal is rejected with `REFERENCE_NOT_FOUND` and a
`WagerTransactionRejected` event.

### When the reference exists but is not usable

| Reference state | Outcome |
|---|---|
| Not found | `PENDING_REFERENCE`, retry with backoff |
| `PENDING` / `PENDING_REFERENCE` | `PENDING_REFERENCE` — it has not taken effect yet, so there is nothing to reverse; keep waiting rather than deciding on an unsettled result |
| `REJECTED` / `FAILED` | Rejected with `REFERENCE_NOT_SUCCESSFUL` — it never moved money |
| `PROCESSED` but disagrees on player, wallet, round, or currency | Rejected with `REFERENCE_MISMATCH` |
| `PROCESSED` with a different amount | Rejected with `REFERENCE_MISMATCH` (partial reversals are out of scope) |
| `PROCESSED`, already reversed | Rejected with `DUPLICATE_REVERSAL` |
| `PROCESSED`, reversible, funds available | Processed |

### Failure codes

Every rejection carries a stable code (`internal/domain/wagertx/failure_code.go`),
and the schema enforces that a `REJECTED`/`FAILED` row has one and that a
successful row does not:

| Code | Meaning | Corrigible by the caller? |
|---|---|---|
| `INSUFFICIENT_BALANCE` | A BET the player cannot afford | Yes — fund the wallet |
| `INSUFFICIENT_BALANCE_FOR_REVERSAL` | A reversal that would need to debit more than the wallet holds | Yes, but a different operational problem |
| `REFERENCE_NOT_FOUND` | The reversal's target never arrived within the budget | Definitive |
| `REFERENCE_MISMATCH` | The target disagrees on provider/player/wallet/round/currency/amount | Definitive |
| `REFERENCE_NOT_SUCCESSFUL` | The target itself was rejected or failed | Definitive |
| `DUPLICATE_REVERSAL` | The target already has a successful reversal | Definitive |
| `CURRENCY_MISMATCH` | The operation's currency is not the wallet's | Definitive |
| `INVALID_AMOUNT` | The amount is not valid for the kind | Yes — resend correctly |

The first two are deliberately distinct codes for the same symptom, because
they describe very different situations to whoever is on the other end.

---

## 6. Reversals

### Why REFUND and ROLLBACK are separate kinds

Applied to a BET they produce the *same* financial movement — a credit of the
exact amount. They are still distinct because their scope differs, and
because they mean different things to the provider that sends them:

| Kind | May reverse | Direction |
|---|---|---|
| `REFUND` | `BET` only | Credit (undoes a debit) |
| `ROLLBACK` | `BET` | Credit (undoes a debit) |
| `ROLLBACK` | `WIN` | Debit (undoes a credit) |
| `ROLLBACK` | `REFUND` | Debit (undoes a credit) |

`reversalDirection` in `internal/app/process_wager.go` is exactly this table.
A REFUND pointed at anything other than a BET is rejected with
`REFERENCE_MISMATCH`; so is a ROLLBACK of an `OPENING` or a `LOSS`.

The amount must match the reference **exactly** — `ref.Money().Equal(tx.Money())`.
Partial reversals are out of scope, and accepting a mismatched amount would
silently invent or destroy money.

### Preventing a double reversal

This is the subtle one. Because a BET can be targeted by both a REFUND and a
ROLLBACK, guarding each kind separately would let both succeed and return the
same debit twice. The guard therefore counts them **together**:

```sql
SELECT EXISTS (
    SELECT 1 FROM wager_transaction
    WHERE reference_transaction_id = $1
      AND kind IN ('REFUND', 'ROLLBACK')
      AND status = 'PROCESSED'
)
```

The question asked is not "has this been refunded?" or "has this been rolled
back?" but "**has this operation already been successfully reversed by
anything?**". If so, the second reversal — of either kind, in either order —
is rejected with `DUPLICATE_REVERSAL`.

This check runs inside the transaction that holds the wallet's row lock, so
two concurrent reversals of the same bet on the same wallet are serialized
and the second one sees the first. Verified end to end: a REFUND of a BET
succeeded, and a subsequent ROLLBACK of the same BET was rejected with
`DUPLICATE_REVERSAL` (`422`).

---

## 7. Inbox and outbox

### The outbox guarantee

Integration events are inserted into the `outbox` table by the *same*
transaction that made the change they announce (`outboxRepo.Insert` receives
the transaction-bound `Querier`). An event therefore cannot exist unless its
cause committed, which is precisely the "never publish before the commit"
requirement — and it holds for free, without any coordination between the
database and the broker.

A separate worker publishes. Nothing in the request path talks to SQS.

### Claiming, with several publishers

Every instance runs an outbox publisher; they contend through the table
rather than through leader election. `outboxRepo.Claim` is a single statement:

```sql
WITH claimed AS (
    SELECT event_id FROM outbox
    WHERE published_at IS NULL
      AND next_attempt_at <= $1
      AND (locked_until IS NULL OR locked_until < $1)
    ORDER BY next_attempt_at
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox o SET locked_by = $3, locked_until = $4
FROM claimed WHERE o.event_id = claimed.event_id
RETURNING …
```

`SKIP LOCKED` gives each publisher a disjoint batch with no blocking.
`locked_until` (30s by default) is what recovers abandoned work: a publisher
that dies mid-batch leaves stale claims that simply become available again
once they expire — no heartbeat, no failure detector.

Failures back off exponentially (1s → 1 minute) via `Reschedule`, which also
clears the claim so any instance can take the next attempt.

### Crash between publishing and recording it

If a publisher sends the message and dies before `MarkPublished`, the claim
expires and another instance republishes. The republish carries the **same
`eventId`**, because the id is a stored column, not something generated at
publish time. Downstream, `MessageDeduplicationId` is set to that same
`eventId`, so SQS FIFO collapses the repeat inside its 5-minute dedup window,
and beyond that window a consumer can still recognize the id. The code
deliberately fails in this direction: a duplicate a consumer can detect is
strictly better than a lost event.

`MessageGroupId` is the event's `aggregate_id`. Events about one wallet stay
strictly ordered; different wallets flow in parallel. A single group would
serialize the entire system's event stream.

### Inbound

The consumer deletes a message **only after** its handling has committed. The
delete runs on a context detached from cancellation, because the work is
already durable and the delete must still go out during shutdown.

Message dispositions:

| Situation | Action |
|---|---|
| Handled (processed, rejected, or parked) | Delete |
| Permanently unprocessable — malformed body, missing `messageId`/`idempotencyKey`, non-UUID ids, an unknown or non-external kind (including `OPENING`), a bad amount, an idempotency or identity conflict, an unknown wallet | **Not** deleted; the redrive policy carries it to the DLQ after `maxReceiveCount` (5) |
| Transient — database unavailable, concurrency conflict | Not deleted; redelivered when the 60s visibility timeout lapses, with SQS providing the backoff |

Leaving a permanently bad message undeleted rather than dropping it is
intentional: the DLQ is where a human can find it. The queues
(`wager-transactions.fifo`, `wager-events.fifo`) and their DLQs are
provisioned with redrive in `deploy/localstack/init-sqs.sh`. Content-based
deduplication is deliberately **off**: producers send an explicit
`MessageDeduplicationId`, and the application's own idempotency is the real
guarantee — SQS's dedup window is a convenience, never something correctness
rests on.

### The outbound contract: what a consumer of `wager-events.fifo` must do

Every message on the outbound queue is one envelope:

```json
{
  "eventId": "uuid",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "uuid",
  "correlationId": "uuid",
  "causationId": "uuid",
  "occurredAt": "2026-09-17T23:14:05.123456789Z",
  "version": 1,
  "data": { }
}
```

| Event | Emitted when | `aggregateId` | `data` carries |
|---|---|---|---|
| `WagerTransactionProcessed` | an operation completes, **including `LOSS`** | transaction id | ids, kind, money, resulting balance, `processedAt` |
| `WagerTransactionRejected` | a business rule refuses it definitively | transaction id | ids, kind, money, `failureCode`, `rejectedAt` |
| `WagerTransactionPendingReference` | a reversal is parked awaiting its target | transaction id | ids, kind, money, the awaited reference, `pendingSince` |
| `WalletBalanceChanged` | the balance actually moves | wallet id | `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion` |

Routing and consumption rules, all of which a consumer has to honour:

- **`MessageGroupId` is the `aggregateId`.** Events about one wallet arrive
  in order relative to each other; different wallets flow in parallel. A
  consumer that needs per-wallet ordering gets it for free, and one that
  does not can process groups concurrently.
- **`MessageDeduplicationId` is the `eventId`**, and it is *stable across
  republishes*. A publisher that dies between a successful send and marking
  the row published will send the same event again with the same id.
- **Delivery is at-least-once. Deduplicate by `eventId`.** This is the one
  non-negotiable requirement on the consumer side: SQS's five-minute dedup
  window catches the common case, but a republish after a long outage will
  get through, and the consumer must recognize it.
- **`version` is the payload schema version**, stamped by the event
  constructor. A consumer should refuse a version it does not understand
  rather than guess.
- **`correlationId` ties every event produced while handling one command**;
  `causationId`, where present, points at the thing that directly caused
  this event — `WalletBalanceChanged` carries the id of the transaction
  that moved the money. The primary events carry no `causationId` because
  they *are* the primary event.
- **Money is always a decimal string with two places plus an ISO 4217
  code** (`{"amount":"25.00","currency":"BRL"}`), never a number, so no
  consumer can parse it into a float by accident.
- **Timestamps are UTC RFC 3339.**

---

## 8. Authentication and authorization

### Why Keycloak

The challenge puts password storage and token issuance out of scope, so the
service must be a pure resource server. Keycloak was chosen over a hosted IdP
because it runs in Docker Compose with no account or network dependency,
speaks standard OAuth 2.0/OIDC (so nothing here is Keycloak-specific beyond
the claim names), and can be provisioned declaratively — the whole realm,
including the test clients, is one imported file
(`deploy/keycloak/jungle-realm.json`), which makes the environment
reproducible from a clean checkout.

The flow is `client_credentials`: these are service-to-service calls with no
human in the loop, so there is no authorization code, no redirect, and no
user session.

### Local JWKS validation vs. introspection

Tokens are validated **locally** against the issuer's public keys, fetched
once through OIDC discovery and cached (`github.com/coreos/go-oidc/v3`).
Signature, audience, and expiry are checked in-process.

The tradeoff is real: introspection (`/token/introspect`) would let the IdP
revoke a token mid-life and have this service notice immediately, at the cost
of a network round trip on *every* request — adding the IdP's availability
and latency to the critical path of every financial operation. Local
validation was chosen because the access token lifespan is short (5 minutes,
set in the realm), which bounds the revocation window to something acceptable,
while keeping the IdP off the hot path entirely. If immediate revocation
became a requirement, introspection with a short-lived cache would be the way
back.

### Permission model

Two realm roles, one custom claim:

| Client | Realm role | `provider_id` claim | May do |
|---|---|---|---|
| `jungle-internal` | `internal-service` | — | Open wallets, read wallets and ledgers, reconcile, read across providers |
| `provider-a` / `provider-b` | `provider` | `provider-a` / `provider-b` | Submit and read **only** their own operations |

`OIDCVerifier.Verify` maps the token onto an `app.Identity`
(`internal/app/identity.go`), which exposes exactly two questions:
`MayActAsProvider(providerID)` and `MayUseWalletOperations()`. A token with no
recognized role, or with the `provider` role but no `provider_id`, is
rejected.

**The authorized provider comes from the token, never from the body.**
`SubmitTransaction` reads `providerId` from the request only to check it
against the identity — `identity.MayActAsProvider(req.ProviderID)` — and
returns `403` on a mismatch. Trusting the body would make the whole model
decorative, since a caller controls what it sends.

### Why a cross-provider read returns 404, not 403

`Queries.GetTransaction` and `GetProviderTransaction` return `ErrNotFound`
when the identity may not act as the owning provider. A `403` would confirm
that a given `(providerId, externalTransactionId)` *exists*, which is itself
information about a competitor's traffic. Making the unauthorized case
indistinguishable from the missing case leaks nothing. The isolation applies
to replays as well, since a replay goes through the same lookup.

### The `OIDC_ADDITIONAL_ISSUERS` allowlist

Keycloak derives the `iss` claim from the host through which the token was
requested. One realm therefore legitimately answers to
`http://keycloak:8080/realms/jungle` inside the Compose network and
`http://localhost:8081/realms/jungle` from the host — the same keys, the same
realm, two spellings.

The easy fix would be `SkipIssuerCheck` with nothing in its place, which
would accept a token from *any* issuer whose signature happened to verify.
Instead, the issuer check is moved into this service: go-oidc's built-in check
is disabled (it only accepts the single discovery URL), and `Verify` requires
`token.Issuer` to be in an explicit allowlist — `OIDC_ISSUER_URL` plus
`OIDC_ADDITIONAL_ISSUERS`. Signing keys still come only from the discovery
document of `OIDC_ISSUER_URL`, so widening the list widens the accepted
*spelling* of one trusted realm, never which keys are trusted.

The health probes (`/health/live`, `/health/ready`) and `/metrics` are
deliberately public: a probe that required credentials could not distinguish
"the service is down" from "the IdP is down", and the metrics carry counters
and timings only — no balances, identifiers, or payloads.

### Authorization at the broker, not only at the API

Token checks protect the HTTP door. They say nothing about the queue, and a
provider that could read `wager-transactions.fifo` directly would see every
other provider's traffic regardless of how careful the API is. So the queues
carry their own resource policies, provisioned in
`deploy/localstack/init-sqs.sh`:

| Queue | Provider principal | Service principal |
|---|---|---|
| `wager-transactions.fifo` | `SendMessage` only | `ReceiveMessage`, `DeleteMessage`, `ChangeMessageVisibility`, `GetQueue*` |
| `wager-events.fifo` | `ReceiveMessage`, `DeleteMessage` | `SendMessage`, `GetQueue*` |

A provider can enqueue work but never consume it or delete someone else's
message; only this service drains the inbound queue, and only this service
publishes events. The principals come from `SQS_PROVIDER_PRINCIPAL` and
`SQS_SERVICE_PRINCIPAL`, defaulting to LocalStack-shaped role ARNs.

Two honest caveats. First, LocalStack accepts and stores these policies but
does not enforce them the way real AWS does, so locally they document the
intent rather than block anything — on AWS the same document is what IAM
evaluates. Second, the client still authenticates with static `test`/`test`
credentials locally (`internal/queue/queue.go`); in a real deployment that
becomes an IAM role assumed by the task, and `SQS_ENDPOINT_URL` is left
empty so the default credential chain takes over. Nothing in the code
changes for that switch.

Domain validation in the consumer is unchanged by any of this: a message
that arrives is still validated field by field, still hashed, still
deduplicated through the inbox. Broker policy decides *who may enqueue*; it
never decides whether the contents are acceptable.

---

## 9. Invariants enforced by the database

These are not merely validated in Go. They are enforced by the schema, so no
application bug, no future code path, and no manual `psql` session can
violate them. Each was verified by **attempting the violation directly in
psql** against the running database; all were refused.

### `wallet`

| Constraint | Makes impossible |
|---|---|
| `wallet_player_currency_unique` | A second wallet for the same `(player_id, currency)` |
| `wallet_balance_non_negative` | Storing a negative balance, however it was computed |
| `wallet_version_positive` | A version below 1 |
| `wallet_currency_format` | A currency that is not three uppercase letters |

### `wager_transaction`

| Constraint | Makes impossible |
|---|---|
| `wager_transaction_idempotency_key_unique` | Two rows sharing an Idempotency-Key |
| `wager_transaction_provider_external_unique` | The same operation recorded twice, even under different keys |
| `wager_transaction_one_opening_per_wallet` (partial unique index) | A duplicate opening credit for a wallet |
| `wager_transaction_origin` | An `OPENING` carrying external metadata, or an external kind missing it — the schema itself distinguishes internal from external |
| `wager_transaction_reference` | A REFUND/ROLLBACK without a reference, or a BET/LOSS/OPENING with one |
| `wager_transaction_failure_code` | A rejection without a code, or a success carrying one |
| `wager_transaction_resulting_balance` | A processed row without a balance snapshot, or a non-processed row with one |
| `kind` / `status` CHECKs | Any unrecognized kind or status |
| **Trigger** `wager_transaction_terminal_guard` | Any `UPDATE` of a row already `PROCESSED`/`REJECTED`/`FAILED` — a finished operation cannot be silently reprocessed |

### `wallet_ledger_entry`

| Constraint | Makes impossible |
|---|---|
| `wallet_ledger_entry_wallet_transaction_unique` | Two ledger entries for the same `(wallet, transaction)` — a double movement |
| `wallet_ledger_entry_balance_math` | An entry where `balance_after ≠ balance_before ± amount` for its direction |
| `amount > 0`, balances `>= 0` | Zero/negative movements and negative snapshots |
| **Triggers** `..._no_update`, `..._no_delete` | Any `UPDATE` or `DELETE`, unconditionally — the ledger is append-only by enforcement, not convention |

### `journal_entry`

| Constraint | Makes impossible |
|---|---|
| `direction IN ('DEBIT','CREDIT')`, `amount_minor_units > 0` | An entry with no side, or a zero/negative posting |
| `transaction_id` foreign key | A journal entry with no transaction behind it |
| **Constraint trigger** `journal_entry_balanced` (deferrable, initially deferred) | Committing a transaction whose entries do not sum to zero — checked at `COMMIT`, so the two halves of a movement may be inserted in either order but neither can be committed alone |
| **Triggers** `..._no_update`, `..._no_delete` | Any `UPDATE` or `DELETE`, as with the wallet ledger |

The deferred constraint trigger is the interesting one: double-entry
balance is not a property of a row, so a row-level `CHECK` cannot express
it. Deferring to commit time lets the check see the whole movement while
still making an unbalanced pair impossible to persist.

### `inbox` / `outbox`

`inbox`'s primary key `(consumer_name, message_id)` is the dedup guarantee
itself. `outbox` carries `event_id` as its primary key, which is what makes a
republish idempotent downstream.

---

## 10. Fx composition and shutdown

### Module per package

Each package exposes its own `fx.Module`; `cmd/jungle/main.go` composes them
and does nothing else. Adapters are provided *as their ports* via
`fx.Annotate(…, fx.As(new(app.UnitOfWork)))`, so the use cases receive
interfaces and never the concrete types.

The **domain** packages are free of Fx, HTTP, SQL, and SQS, as required.
`internal/app` does declare an `fx.Module` — the challenge explicitly asks for
use cases to be composed with Fx — but it contains only wiring; the use case
types themselves take plain constructor arguments and are usable without Fx,
which is what keeps them testable.

`cmd/jungle/main_test.go` runs `fx.ValidateApp` over the exact composition
`main()` uses, so a missing provider or a wrong annotation fails at test time
rather than at boot.

One recurring trap, recorded here because it cost time: a dependency provided
but consumed by nobody is never constructed by Fx. `*http.Server`,
`*pgxpool.Pool`, and `*sqs.Client` each need an `fx.Invoke(func(*T) {})` in
their own module, or the app starts without them.

### Worker lifetimes

`internal/worker/worker.go` starts each loop in `OnStart` and stops it in
`OnStop`. Worker contexts derive from `context.Background()`, **not** from
the `OnStart` context — the latter is cancelled as soon as startup finishes,
which would kill every worker immediately. A worker's lifetime is the
application's lifetime, ended explicitly by `OnStop`.

Each worker receives **two** contexts rather than one, because "stop taking
on new work" and "abandon what you are holding" are different instructions
and the challenge asks for both to be distinguishable:

| Context | Cancelled | Meaning |
|---|---|---|
| `Fetch` | the instant shutdown begins | stop pulling new work: no more `ReceiveMessage`, no more ticks |
| `Work` | only after the shutdown budget elapses | finish and commit what is already in flight |

A single shared context would make the budget meaningless: cancelling it
would abort the in-flight transaction along with the fetch loop, so the
service could never actually *conclude* anything during shutdown — only
roll it back.

### Shutdown sequence

On SIGTERM, Fx runs `OnStop` hooks in reverse dependency order:

1. Each worker's `Fetch` context is cancelled. The consumer stops asking for
   new messages; the ticking workers stop between passes.
2. Work already in flight keeps running on `Work` for up to
   `SHUTDOWN_TIMEOUT` (20s by default). Completion is reported through a
   `done` channel, so the stop is observable rather than a blind sleep;
   exceeding the budget logs a warning, cancels `Work`, and proceeds.
3. Messages the consumer had already fetched but had not started are
   released with `ChangeMessageVisibility(0)`, so they are redelivered
   immediately instead of waiting out the 60s visibility timeout.
4. The HTTP server shuts down gracefully.
5. Only then are the pool and clients closed — after the components that use
   them have stopped.

**Abandoning unfinished work at the deadline is still safe**, and that is
why the budget can be bounded at all: anything not finished was never
committed. An SQS message whose handling did not commit is simply not
deleted, and comes back when its visibility timeout lapses. An outbox row
whose publish did not complete keeps its claim until it expires and is then
picked up by another instance. The recovery mechanisms are the same ones
that handle a hard crash, so a slow shutdown degrades into a crash rather
than into corruption.

`cmd/jungle/lifecycle_test.go` exercises this end to end: it starts the real
application with `RequireStart`, confirms it serves, stops it with
`RequireStop`, and then asserts the listener was released and no goroutines
leaked.

---

## 11. Observability

### Logs

Structured JSON throughout (zap), with `fxevent.ZapLogger` folding Fx's own
startup and shutdown events into the same stream, so one log pipeline sees
everything.

The challenge names five identifiers that must make an operation traceable.
Every settled operation emits one line carrying all of them:

```json
{
  "level": "info",
  "msg": "wager operation settled",
  "source": "sqs",
  "messageId": "msg-123",
  "correlationId": "1124213b-5bd0-4243-a8e4-fed436c45f9b",
  "providerId": "provider-a",
  "externalTransactionId": "obs-sqs-1",
  "walletId": "4225f112-adc1-4418-88e9-8f05384b8c24",
  "playerId": "64ff8481-9654-4119-b578-b06ae3018575",
  "transactionId": "29cb363d-cff4-4370-8a40-f59d82582139",
  "kind": "BET",
  "status": "PROCESSED",
  "idempotentReplay": false
}
```

`source` distinguishes the HTTP and SQS doors; `messageId` appears only on
the SQS side, where it exists. A rejection adds `failureCode`, and a
failure logs at `warn` with the error attached.

`correlationId` is taken from the caller's `X-Correlation-Id` when present
and minted otherwise, is echoed back on the response header, is stamped on
every event the operation produces, and is logged by the request middleware
as well — so one identifier ties the HTTP access log, the domain log line,
and the published events together.

Handlers never return an internal error to the caller. They attach it to
the request (`c.Error`) and the middleware logs it, so a generic `503` body
still leaves a diagnosable trail.

### What is deliberately not logged

Tokens, secrets and `Authorization` headers never reach a log statement,
and no code path logs a request body. Money appears in logs in exactly one
place — a reconciliation divergence, where the stored balance, the
recomputed balance and the difference are the entire point of the alert.

### Metrics

Exposed at `/metrics` in Prometheus format, unauthenticated because they
carry counters and timings only. Both doors record the same instruments —
`internal/httpapi/metrics.go` and `internal/queue/consumer.go` — so the
`source` label is what separates HTTP from SQS, not a gap in coverage.

| Metric | Type | Answers |
|---|---|---|
| `jungle_wager_transaction_results_total{kind,status,source}` | counter | what the service is doing, and how HTTP compares to SQS |
| `jungle_duplicate_operations_total{mechanism}` | counter | how much traffic is repeats, and which layer caught them |
| `jungle_concurrency_conflicts_total` | counter | contention on the version compare-and-swap |
| `jungle_wager_processing_seconds{source}` | histogram | settle latency per door |
| `jungle_message_retries_total` | counter | transient failures handed back to the queue |
| `jungle_messages_dead_lettered_total` | counter | messages abandoned as unprocessable |
| `jungle_outbox_published_total` | counter | integration events that went out |
| `jungle_outbox_retries_total` | counter | publish attempts that failed and backed off |
| `jungle_outbox_lag_seconds` | gauge | age of the oldest unpublished event |
| `jungle_pending_references` | gauge | reversals parked awaiting their target |
| `jungle_permanent_failures_total` | counter | transactions recorded `FAILED` for audit |
| `jungle_reconciliation_divergences_total` | counter | wallets whose balance disagreed with the ledger |
| `jungle_auth_failures_total{reason}` | counter | rejected credentials, by reason |

Two of these are the ones worth alerting on. `jungle_outbox_lag_seconds`
rising means events are being produced faster than they are published, or
that every publisher is stuck — downstream systems are drifting out of date
and nothing else reveals it. `jungle_reconciliation_divergences_total`
leaving zero means the stored balance and the ledger disagree, which should
be impossible and is the one number that indicates real damage.

### Health

`/health/live` reports only that the process is up, touching no
dependency: a liveness probe that failed whenever Postgres hiccuped would
have the orchestrator kill a healthy process. `/health/ready` checks
Postgres and SQS and reports which one is down, so an instance removes
itself from rotation instead of failing requests.

### Tracing

OpenTelemetry, exported over OTLP/gRPC to Jaeger. It is controlled by
`TRACING_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` and
`TRACING_SAMPLE_RATIO`; disabled, the provider degrades to a no-op, so the
service runs normally with no collector present. Spans are sampled
`ParentBased(TraceIDRatioBased)` — a ratio of 1.0 in development, and a
decision taken upstream is honoured rather than re-rolled per service.

Four seams are instrumented:

- **HTTP** — `otelgin` on the Gin router, with `/health/*` and `/metrics`
  filtered out so probe traffic does not drown the traces.
- **Postgres** — `otelpgx` on the pgx pool, so every statement inside a unit
  of work is a child span. A slow `SELECT ... FOR UPDATE` then reads as
  queueing on a contended wallet instead of as unexplained handler latency.
- **Use case** — `ProcessWager <KIND>` is one span carrying provider,
  external id, kind, wallet, player, final status, failure code and whether
  the call was an idempotent replay.
- **Messaging** — the producer injects W3C `traceparent` into the SQS message
  attributes and the consumer extracts it, so a published event and its
  eventual consumption sit in the same trace.

The outbox is the one hop where a trace has to survive a database round
trip. The publishing goroutine is not the one that accepted the request, so
the context is captured as `trace_parent` on the outbox row at insert time
(migration `000006`) and restored by the publisher before it opens its
producer span. Without that column every integration event would start an
unrelated trace, and the causal link from "this HTTP call" to "that event
downstream" — the thing tracing exists to show here — would be lost.

Tracing complements `correlationId` rather than replacing it:
`correlationId` is the business key that ties records together and survives
in logs, events and the database; the trace id ties *timings* together and
lives only as long as the trace backend keeps it.

### Dashboards

`deploy/grafana/dashboards/jungle-wallet-ledger.json` is provisioned
automatically, datasource included — nothing is imported by hand. Its rows
mirror the failure modes this system actually has: health at a glance
(operations/s, share rejected, duplicates/s, reconciliation divergences,
permanent failures), throughput and settle-latency percentiles broken down
by entry point, messaging (outbox lag, published vs. retried, redeliveries
and DLQ, pending references), and contention and access (concurrency
conflicts, duplicates by mechanism, auth failures by reason).

---

## 12. Limitations, interpretations, and work not finished

Stated plainly, because a reviewer will find these anyway.

### Interpretations adopted

- **A WIN's reference is optional, informational metadata.** The challenge
  says a WIN *may* reference a bet from the same round. It is resolved
  best-effort (`attachOptionalReference`): if it resolves, the internal id is
  recorded; if it is absent or unresolvable, the payout proceeds regardless.
  The reference is deliberately **not** validated for agreement and never
  blocks a WIN, since the challenge does not make it a precondition. A
  stricter reading would validate it and reject on mismatch.
- **A rejection is a committed result.** Business rejections persist their
  transaction row and emit an event, so they are replayable and auditable,
  and are returned as `422` with the `failureCode` — not as a `4xx` with an
  empty body.
- **`PENDING_REFERENCE` is reported as `202 Accepted`**, as durable
  acceptance of work still being settled.
- **Cross-provider reads return `404`, not `403`** (reasoning in §8).
- **Zero initial balance creates no OPENING**, no ledger entry, and no
  financial events — only the wallet row.

### How the claims here were verified

Everything this document asserts about behaviour is exercised by
`make test-integration`, which runs against the real PostgreSQL, LocalStack
and Keycloak from `docker-compose.yml` — no mock stands in for
infrastructure. 47 tests across six files:

| File | Covers |
|---|---|
| `concurrency_test.go` | the two mandatory scenarios (two 80.00 bets on a 100.00 wallet; the same operation sent 50 times in parallel), independent wallets progressing in parallel, append-only enforcement, frozen terminal rows, two publishers never double-sending, republish keeping the same `eventId` |
| `wagering_test.go` | the operation matrix per kind, reversals and their distinct failure codes, all three idempotency layers, HTTP→SQS cross-channel identity, a late reference resolving and an absent one expiring into `REFERENCE_NOT_FOUND`, reconciliation, provider isolation |
| `consumer_test.go` | delete-after-commit, redelivery dedup, a redelivery whose payload hash changed being refused, malformed messages reaching the DLQ, the queue racing the HTTP path on one operation, and a crash injected between commit and delete |
| `auth_test.go` | real Keycloak tokens: missing/invalid credentials, an expired one (a client whose tokens live one second), a foreign issuer, internal-only routes, one provider acting as another, status code per outcome |
| `recovery_test.go` | the instance that did the work is shut down and a fresh one takes over: idempotency, the inbox, the balance and the ledger survive the restart, and a parked reversal accepted by one instance is settled by another |
| `journal_test.go` | balanced pairs per movement, no entries for a LOSS or a rejection, global balance of zero, the journal agreeing with the wallet, the database refusing an unbalanced pair, append-only |

Unit tests (`make test-race`) cover the domain (`money`, `wallet`,
`wagertx`, `journal`) and the payload hash. `cmd/jungle/main_test.go` validates the Fx
graph without starting anything, and `cmd/jungle/lifecycle_test.go` — also
behind the `integration` tag — starts and stops the whole application
against live dependencies, which is what catches a hook that blocks or a
worker that outlives shutdown. The database constraints in §9 were
additionally attempted by hand in `psql`, which is how several of them were
shown to hold against a path the application code cannot take.

### Known gaps

- **The journal is written but not yet read at runtime.** Balances and the
  reconciliation endpoint are computed from `wallet_ledger_entry`;
  `journal_entry` is posted in the same commit and its integrity is asserted
  by tests (`GlobalImbalance`, per-account balances), but no endpoint
  exposes it and no periodic job checks global balance in a running system.
  A reviewer should read it as an audit-grade record with enforced
  invariants, not as the balance source of truth.
- **Tracing samples everything by default.** `TRACING_SAMPLE_RATIO` defaults
  to `1.0`, which is right for a demo and wrong for production volume; there
  is no tail-based or error-biased sampling, so lowering the ratio would
  drop failed operations at the same rate as successful ones.
- **Single currency in practice.** Every type carries its currency and
  mismatches are rejected throughout, but only BRL is exercised, and
  `ParseNonNegative` is the only currency-aware entry point wired into the
  API. Nothing converts between currencies, by design.
- **The inbox's two timestamps are currently redundant.** `received_at` and
  `completed_at` are always written with the same value, because the row is
  inserted in the same transaction as the work it records. They are kept
  separate because they mean different things and would diverge if a
  claim-before-processing step were ever added.
- **The outbox publisher is sequential, one round trip per event.** Each
  event costs one `SendMessage` plus one transaction to mark it published,
  which measures at about 300 events per second on a laptop while the write
  path in the same run produced roughly 900. Under sustained load the backlog
  therefore grows and `jungle_outbox_lag_seconds` climbs with it: 66 seconds
  by the end of a 100-second run. A `RunOnce` that drains repeatedly instead
  of publishing one batch per tick took this from 42 events per second to
  300, but the real fix is `SendMessageBatch` (10 per call) with a single
  batched `MarkPublished` — identified, not implemented. Steady-state lag is
  zero; this only bites under sustained burst, and it is throughput, not
  correctness: the events are committed with their cause and published late,
  never lost or published early. The run, with numbers and caveats, is in
  [`deploy/loadtest/RESULTS.md`](deploy/loadtest/RESULTS.md).
- **`FAILED` is only produced by the reference worker.** A permanent
  infrastructure failure is recorded for audit when a parked reversal
  exhausts its attempts against a failing dependency (see §5). The
  synchronous HTTP and SQS paths still roll back and surface a retryable
  `503` or leave the message for redelivery instead, because at that point
  nothing has been committed and there is no durable record to mark — the
  request itself is the thing to retry.
- **Broker policies are enforced by the broker, not asserted by a test.**
  The queue policies in §8 are provisioned and inspectable, but LocalStack
  does not evaluate SQS resource policies the way real AWS does, so no test
  proves an unauthorized principal is refused. On real AWS this is IAM's
  job, and the policy shape is what carries it.
