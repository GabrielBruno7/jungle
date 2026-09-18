import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Counter, Rate, Trend } from 'k6/metrics';

const API = __ENV.API_URL || 'http://app:8080';
const KEYCLOAK = __ENV.KEYCLOAK_URL || 'http://keycloak:8080';
const REALM = __ENV.REALM || 'jungle';

const WALLET_POOL = Number(__ENV.WALLET_POOL || 50);
const CONTENDED_WALLETS = Number(__ENV.CONTENDED_WALLETS || 3);
const DURATION = __ENV.DURATION || '60s';

const processed = new Counter('jungle_processed');
const rejected = new Counter('jungle_rejected_422');
const pending = new Counter('jungle_pending_202');
const conflicts = new Counter('jungle_conflict_409');
const replays = new Counter('jungle_idempotent_replays');
const transportErrors = new Counter('jungle_transport_errors');
const authErrors = new Counter('jungle_auth_errors');
const serverErrors = new Counter('jungle_server_errors');
const brokenRate = new Rate('jungle_broken_rate');
const settleTime = new Trend('jungle_settle_ms', true);

export const options = {
  discardResponseBodies: false,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    spread: {
      executor: 'ramping-arrival-rate',
      startRate: 40,
      timeUnit: '1s',
      preAllocatedVUs: 40,
      maxVUs: 150,
      stages: [
        { target: 80, duration: '20s' },
        { target: 160, duration: '20s' },
        { target: 160, duration: DURATION },
      ],
      exec: 'spread',
      tags: { scenario_name: 'spread' },
    },
    contention: {
      executor: 'constant-vus',
      vus: 20,
      duration: DURATION,
      startTime: '40s',
      exec: 'contention',
      tags: { scenario_name: 'contention' },
    },
    replay: {
      executor: 'constant-vus',
      vus: 5,
      duration: DURATION,
      startTime: '40s',
      exec: 'replay',
      tags: { scenario_name: 'replay' },
    },
  },
  thresholds: {

    jungle_broken_rate: ['rate<0.01'],
    jungle_transport_errors: ['count==0'],
    jungle_auth_errors: ['count==0'],
    jungle_server_errors: ['count==0'],
    'http_req_duration{scenario_name:spread}': ['p(95)<1500'],
    'http_req_duration{scenario_name:replay}': ['p(95)<1000'],

    'http_req_duration{scenario_name:contention}': ['p(95)<5000'],
    checks: ['rate>0.99'],
  },
};

function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

function fetchToken(clientId, clientSecret) {
  const res = http.post(
    `${KEYCLOAK}/realms/${REALM}/protocol/openid-connect/token`,
    { grant_type: 'client_credentials', client_id: clientId, client_secret: clientSecret },
    { tags: { name: 'keycloak_token' } },
  );
  if (res.status !== 200) {
    throw new Error(`token request for ${clientId} returned ${res.status}: ${res.body}`);
  }
  return res.json('access_token');
}

let cachedToken = null;
let cachedUntil = 0;

function providerToken() {
  const now = Date.now();
  if (cachedToken && now < cachedUntil) {
    return cachedToken;
  }
  cachedToken = fetchToken('provider-a', 'provider-a-secret');
  cachedUntil = now + 4 * 60 * 1000;
  return cachedToken;
}

export function setup() {
  const runId = uuid().slice(0, 8);
  const internal = fetchToken('jungle-internal', 'internal-secret');

  const headers = {
    Authorization: `Bearer ${internal}`,
    'Content-Type': 'application/json',
  };

  const wallets = [];
  for (let i = 0; i < WALLET_POOL; i++) {
    const playerId = uuid();
    const res = http.post(
      `${API}/wallets`,
      JSON.stringify({
        playerId,
        initialBalance: { amount: '1000000.00', currency: 'BRL' },
      }),
      { headers, tags: { name: 'open_wallet' } },
    );
    if (res.status !== 201) {
      throw new Error(`opening wallet ${i} returned ${res.status}: ${res.body}`);
    }
    wallets.push({ walletId: res.json('id'), playerId });
  }

  return {
    runId,
    wallets,
    contended: wallets.slice(0, CONTENDED_WALLETS),
  };
}

function submit(data, wallet, kind, amount, externalId, key) {
  const body = JSON.stringify({
    providerId: 'provider-a',
    externalTransactionId: externalId,
    playerId: wallet.playerId,
    walletId: wallet.walletId,
    roundId: `round-${externalId}`,
    gameId: 'load-test',
    kind,
    money: { amount, currency: 'BRL' },
  });

  const res = http.post(`${API}/wagering/transactions`, body, {
    headers: {
      Authorization: `Bearer ${providerToken()}`,
      'Content-Type': 'application/json',
      'Idempotency-Key': key,
    },
    tags: { name: 'submit_transaction', kind },
  });

  classify(res);
  return res;
}

function classify(res) {
  settleTime.add(res.timings.duration);

  if (res.status === 0) {
    transportErrors.add(1);
    brokenRate.add(true);
    return;
  }

  if (res.status === 200) {
    processed.add(1);
    if (res.json('idempotentReplay') === true) {
      replays.add(1);
    }
    brokenRate.add(false);
    return;
  }

  if (res.status === 202) {
    pending.add(1);
    brokenRate.add(false);
    return;
  }

  if (res.status === 422) {
    rejected.add(1);
    brokenRate.add(false);
    return;
  }

  if (res.status === 409) {
    conflicts.add(1);
    brokenRate.add(false);
    return;
  }

  if (res.status === 401 || res.status === 403) {
    authErrors.add(1);
    brokenRate.add(true);
    return;
  }

  if (res.status >= 500) {
    serverErrors.add(1);
    brokenRate.add(true);
    return;
  }

  brokenRate.add(true);
}

function acceptable(res) {
  return [200, 202, 422].includes(res.status);
}

export function spread(data) {
  const wallet = data.wallets[Math.floor(Math.random() * data.wallets.length)];
  const id = `${data.runId}-sp-${exec.vu.idInTest}-${exec.scenario.iterationInTest}`;

  const roll = Math.random();
  let kind = 'BET';
  let amount = '1.00';
  if (roll > 0.85) {
    kind = 'LOSS';
    amount = '0.00';
  } else if (roll > 0.6) {
    kind = 'WIN';
    amount = '0.50';
  }

  const res = submit(data, wallet, kind, amount, id, `provider-a:${id}`);
  check(res, { 'spread: answered with a settled status': acceptable });
}

export function contention(data) {

  const wallet = data.contended[exec.scenario.iterationInTest % data.contended.length];
  const id = `${data.runId}-ct-${exec.vu.idInTest}-${exec.scenario.iterationInTest}`;

  const res = submit(data, wallet, 'BET', '1.00', id, `provider-a:${id}`);
  check(res, { 'contention: answered with a settled status': acceptable });
}

let seeded = null;

export function replay(data) {
  if (seeded === null) {
    const wallet = data.wallets[exec.vu.idInTest % data.wallets.length];
    const id = `${data.runId}-rp-${exec.vu.idInTest}`;
    seeded = { wallet, id, key: `provider-a:${id}` };

    const first = submit(data, seeded.wallet, 'BET', '2.00', seeded.id, seeded.key);
    check(first, { 'replay: seed accepted': (r) => r.status === 200 });
    return;
  }

  const res = submit(data, seeded.wallet, 'BET', '2.00', seeded.id, seeded.key);
  check(res, {
    'replay: recognized as a replay': (r) => r.status === 200 && r.json('idempotentReplay') === true,
  });
}
