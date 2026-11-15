# Life Timeline & Habit Engine

Migrations `006_life_timeline_habits.sql`, `007_adaptive_checkin_scheduler.sql`, and `008_checkin_delivery_bindings.sql` add personal activity events, revision history, request idempotency, habits, opt-in check-in settings, durable check-in request states, trusted WhatsApp bindings, delivery attempts, and inbound response deduplication. They are additive and do not alter finance, memory, or couple workspace data.

## Local migration

```bash
DATABASE_URL='postgres://...' go run ./cmd/migrate migrate
```

Do not run this against production Neon until the migration has been reviewed and a backup/restore path is confirmed. The migrator verifies checksums. If a migration has already been applied, restore its original file and use a new forward-fix migration; do not edit an applied migration.

## Activity API

All requests use the normal Bearer credential and personal timeline permission. Mutations accept `Idempotency-Key` (or `idempotency_key` in JSON). Activity timestamps are RFC3339 `timestamptz` values.

```bash
curl -X POST "$MIMO_API_URL/v1/activities/batch" \
  -H "Authorization: Bearer $MIMO_API_TOKEN" \
  -H "Idempotency-Key: wa-message-123" -H 'Content-Type: application/json' \
  -d '{"activities":[{"title":"Meeting","category":"work","started_at":"2026-11-16T09:00:00+07:00","ended_at":"2026-11-16T10:00:00+07:00"}]}'

curl "$MIMO_API_URL/v1/activities?from=2026-11-16T00:00:00%2B07:00&limit=200&offset=0"
curl "$MIMO_API_URL/v1/timeline/coverage?from=2026-11-16T09:00:00%2B07:00&to=2026-11-16T12:00:00%2B07:00"
```

Coverage unions overlapping intervals before calculating hourly buckets. Empty buckets are reported as unknown; they are not classified as sleep, failure, or unproductive time. Open events are clipped to the effective current time.

## Check-in safety and P2B delivery

Settings default to disabled. The API persists settings and the request state model. P2B includes a provider adapter boundary and trusted binding checks, but outbound WhatsApp dispatch remains disabled until staging verifies the installed provider contract and recipient mapping.

P2A adds deterministic scheduler evaluation in the existing worker. It evaluates enabled personal settings on the configured interval (`MIMO_CHECKIN_EVALUATION_INTERVAL_MINUTES`, default 15), queues durable reminders, suppresses queued reminders after timeline backfill, and enforces quiet hours, cooldown, local-day limits, unanswered pause, and overlap deduplication.

P2B adds backend-owned bindings in `mimo.checkin_delivery_bindings`. Each enabled binding joins one human-owned personal workspace, one non-Nara agent (`neura` or `najwa`), one configured WhatsApp account, and one verified direct JID ending in `@s.whatsapp.net`. Groups, broadcasts, newsletters, arbitrary tool destinations, and couple workspaces are rejected.

Outbound remains disabled by default:

```text
MIMO_CHECKIN_OUTBOUND_ENABLED=false
MIMO_CHECKIN_DELIVERY_MODE=fake
MIMO_CHECKIN_ALLOWED_PRINCIPALS=
MIMO_CHECKIN_MAX_OUTBOUND_PER_MINUTE=1
```

With `cli` mode and an explicit allowlist, the worker invokes OpenClaw using argument arrays and a timeout. Go renders the deterministic template; no LLM is invoked. Provider acceptance is distinct from delivery/read state. Durable attempts use `started`, `provider_accepted`, `confirmed_failed`, or `unknown`; timeout, interruption, and unrecognized output become `delivery_unknown` and are never blindly retried. No verified receipt reconciliation endpoint is available in this repository, so unknown attempts require operator review.

Inbound response processing correlates by reply-to provider message ID, otherwise requires exactly one active sent check-in for the same verified DM. It supports multiple time ranges, `skip`, `nanti`, and `ingatkan lagi 1 jam`; activity writes and `answered` share one PostgreSQL transaction. Duplicate provider message IDs are idempotent. The signed gateway ingress adapter is implemented but disabled by default.

## Staging inbound gateway runbook

The internal endpoint is `POST /internal/v1/checkin/inbound`. It is disabled unless both `MIMO_CHECKIN_INBOUND_ENABLED=true` and a non-empty `MIMO_CHECKIN_INBOUND_SECRET` are configured. The gateway signs `timestamp + "." + raw_body` with HMAC-SHA256 and sends the hex digest as `X-Mimo-Signature: sha256=...` plus a Unix-seconds `X-Mimo-Timestamp` header. Requests older than five minutes, malformed JSON, unknown fields, oversized bodies, non-DM JIDs, or invalid signatures are rejected. The event must contain trusted transport metadata (`event_id`, `channel`, `account_id`, `sender_jid`, `message_id`, optional reply ID, text, and optional RFC3339 `received_at`); it must not contain owner or workspace claims.

Controlled one-recipient verification:

1. Keep `MIMO_CHECKIN_OUTBOUND_ENABLED=false` and `MIMO_CHECKIN_DELIVERY_MODE=fake`. Provision exactly one verified `checkin_delivery_bindings` row for the approved staging principal and direct JID, and confirm the personal workspace/agent memberships.
2. Set a staging-only inbound secret through the secret manager, set `MIMO_CHECKIN_INBOUND_ENABLED=true`, deploy the API only, and confirm the endpoint returns 404 with the flag off and 401 for an invalid signature.
3. Create a queued/sent check-in using the fake adapter or a staging fixture. Send one signed `skip` or activity reply from the bound DM. Confirm one inbound row, the expected state transition, and activity rows in one transaction.
4. Replay the identical signed event. Confirm HTTP success with `duplicate:true` and no additional activity rows. Test a group JID, another account, a different user, stale timestamp, and forged signature; all must be rejected.
5. Keep outbound disabled until the installed OpenClaw transport command, account routing, provider acceptance response, and operator kill switch have been verified separately. No automated or runbook step in this repository sends a live WhatsApp message.

Example staging-only signed request (use a test message ID and the approved staging DM):

```bash
export MIMO_CHECKIN_INBOUND_ENABLED=true
export MIMO_CHECKIN_INBOUND_SECRET='load-from-staging-secret-manager'
BODY='{"event_id":"staging-1","channel":"whatsapp","account_id":"verified-staging-account","sender_jid":"628000000000@s.whatsapp.net","message_id":"staging-msg-1","reply_to_provider_message_id":"provider-checkin-1","text":"skip"}'
TS="$(date +%s)"
SIG="sha256=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$MIMO_CHECKIN_INBOUND_SECRET" -hex | awk '{print $2}')"
curl --fail-with-body -X POST "$MIMO_API_URL/internal/v1/checkin/inbound" \
  -H "Content-Type: application/json" -H "X-Mimo-Timestamp: $TS" -H "X-Mimo-Signature: $SIG" \
  --data "$BODY"
```

Rollback and incident handling: first set `MIMO_CHECKIN_INBOUND_ENABLED=false` and `MIMO_CHECKIN_OUTBOUND_ENABLED=false`; do not delete bindings or migration history. Stop or roll back only the feature API/worker after queued and `delivery_unknown` rows have been inspected. Never replay an unknown delivery automatically. Preserve sanitized request IDs, event IDs, check-in IDs, and delivery-attempt IDs for investigation, but do not log message text, credentials, signatures, or recipient data. If a binding is suspected, set its `enabled` or `verified` flag false and rotate the gateway secret through the secret manager.

`internal/checkin.FakeDelivery` exercises claiming, success, confirmed failure, uncertain delivery, and lease recovery. Automated tests send no real WhatsApp messages. The installed OpenClaw package was inspected locally, but a stable authenticated outbound Gateway contract and receipt schema could not be verified; CLI behavior must be confirmed in staging before enabling `cli` mode.

## Verification

```bash
GOCACHE="$PWD/.gocache" go test ./...
cd openclaw-plugin && npm run build && npm test
```

PostgreSQL integration tests require a disposable local `pgvector/pgvector` database and explicitly reject non-local hosts:

```bash
docker run --rm --name mimo-test-pg -e POSTGRES_PASSWORD=testpass \
  -e POSTGRES_DB=mimo_test -p 127.0.0.1:55432:5432 -d pgvector/pgvector:pg17
export MIMO_MIGRATION_TEST_DATABASE_URL='postgres://postgres:testpass@127.0.0.1:55432/mimo_test?sslmode=disable'
go test ./cmd/migrate -run TestMigrationIntegration -count=1 -v
go test ./internal/app -run 'TestLifeTimelinePostgresIntegration|TestMultiUserIsolationIntegration' -count=1 -v
go test ./internal/checkin -run TestSchedulerPostgresIntegration -count=1 -v
docker rm -f mimo-test-pg
```

Run the two Go integration commands sequentially because both repository-standard suites reset the `mimo` schema. Never set this URL to Neon production.

For the local container used during development, an equivalent disposable command is:

```bash
export MIMO_MIGRATION_TEST_DATABASE_URL='postgres://postgres:12345678@127.0.0.1:5433/mimo_test?sslmode=disable&default_query_exec_mode=exec'
go test ./cmd/migrate -run TestMigrationIntegration -count=1 -v
go test ./internal/checkin -run TestSchedulerPostgresIntegration -count=1 -v
```

Run integration packages sequentially because each resets the `mimo` schema. Provision a staging binding only after independently verifying the account and DM recipient; initially allow exactly one approved principal. To stop delivery immediately, set `MIMO_CHECKIN_OUTBOUND_ENABLED=false`; queued jobs remain durable and are not sent while disabled.

Smoke-test scenarios: retry a batch with the same key, reuse it with a changed payload (409), correct with the current version then retry a stale version (409), log overlapping events and inspect coverage, and verify a Nara credential cannot create or read personal timeline data.

## Agent instruction snippets

For Neura and Najwa:

> Use Mimo activity tools for timeline facts. Always use the API response as confirmation. Never invent timestamps or durations, never create a habit from a suggestion without user approval, and ask for clarification when an event cannot be identified confidently. Use timeline SQL aggregates for statistics; use memory search only for contextual retrieval.

For both agents, an in-progress activity is logged with `ended_at` omitted and is closed only when the user explicitly says it ended. A natural-language check-in response may contain multiple activities and should be sent as one atomic batch.

## Ubuntu VM deployment checklist

```bash
cd /opt/mimo-personal-os
git fetch origin
git checkout feat/life-timeline-habit-engine
git pull --ff-only origin feat/life-timeline-habit-engine
docker compose run --rm api ./mimo-migrate migrate
docker compose build api worker
docker compose up -d api worker
docker compose logs --tail=100 api worker
```

The feature branch must be reviewed and merged before these commands are used in a production VM. Do not restart an existing OpenClaw gateway as part of migration; deploy the plugin separately and run the smoke tests first.
