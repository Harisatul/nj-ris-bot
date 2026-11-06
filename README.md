# Mimo Personal OS — Go backend + Neon + R2 + OpenClaw

**A functional personal-only MVP starter** for personal knowledge (work notes and ideas), object inventory, image/PDF archiving, searchable long-term memory, and structured money tracking in IDR. One OpenClaw agent calls a small Go API; the LLM is **not** the source of truth for balances or records.

> This project was authored for Go 1.25+ and OpenClaw's native plugin format. Runtime dependencies cannot be downloaded in the build environment where this ZIP was generated, so a full `go test`/Docker image build and compatibility test with your OpenClaw Gateway **have not been run**. Do those checks on your VM before saving important data. See `CHECKLIST.md`.

## What's inside

- `cmd/api` — Go API, limited to owner identity in backend config, Bearer API token, JSON validation
- `cmd/migrate` — checksum-tracked, advisory-locked migration runner with legacy baseline and status commands
- `cmd/backfill` — owner-scoped, idempotent semantic-memory/note queue synchronization CLI
- `cmd/worker` — durable background indexing through the separately deployed Ollama EmbeddingGemma service
- `internal/app` — notes CRUD with revisions, objects CRUD, memory write/list/delete, R2 media upload, search, finance accounts/categories/transactions/summary
- `openclaw-plugin` — thin TypeScript native OpenClaw tools calling HTTP Go backend
- `db/migrations` — database migration using PostgreSQL, pg_trgm and pgvector
- `scripts/backfill.sql` — index existing v0.2 personal memories

## Prerequisites

1. A Neon PostgreSQL project where `vector` and `pg_trgm` are allowed.
2. Go 1.25+ (local), or Docker Compose on the **remote VM**.
3. Optional Cloudflare R2 account + private bucket to store photos/PDFs.
4. Optional Ollama embedding service with `embeddinggemma-2:440m` (768 dimensions). Mimo does not install Ollama or download model weights.
5. OpenClaw already connected to your own WhatsApp. Restrict allowed senders to yourself.

## Quick start on VM (Docker recommended)

```bash
unzip mimo-personal-os.zip
cd mimo-personal-os
cp .env.example .env
# Edit .env with Neon DATABASE_URL, MIMO_API_TOKEN, MIMO_OWNER_ID,
# and optionally R2 + embedding settings.
chmod 600 .env

# Verify module installation and compile on a machine with internet:
# docker compose build

# Apply DB migration (use Neon direct connection URL here)
docker compose run --rm api mimo-migrate

# Start API + optional embedding worker
docker compose up -d
curl -fsS http://127.0.0.1:8080/healthz
```

`DATABASE_URL` should be direct for migrations and can be pooled for the API. If you want separate URLs, temporarily set direct URL when running the migration, then change it to the pooled URL before starting `compose`. **Don't put `.env` in source control.** For an existing v0.2 database, the migration runner validates and baselines historical `001_init.sql`/`002_finance_liabilities.sql` instead of re-executing them. See [docs/migrations.md](docs/migrations.md) for `status`, `baseline`, recovery, and checksum rules.

Alternatively run locally using:

```bash
set -a; . ./.env; set +a
# Downloads Go dependencies (requires network)
go mod tidy
go run ./cmd/migrate
go run ./cmd/api
# Separate terminal: go run ./cmd/worker
```

Go API binds to loopback by default. Compose binds the published port **only to host 127.0.0.1**. The API must not be exposed through a public port or Cloudflare unauthenticated route.

## Finance v0.2: liabilities and installments

Finance accounts are classified as `asset` or `liability`. Use `cash`, `bank`, and `ewallet` for assets; use `paylater`, `credit_card`, or `loan` for liabilities. Existing accounts default safely to `asset` during migration. Liability opening balances represent debt already owed and do not create income or spending. Credit limits are metadata only and never increase assets.

`POST /v1/finance/liability-purchases` records a purchase expense and increases the liability without reducing bank cash. It creates an immutable debt parent and rounded installment schedule. `POST /v1/finance/installments/{id}/payments` creates one debt repayment transaction, atomically allocates it across the oldest unpaid installments, supports partial payments, and rejects overpayment. Repayment reduces asset cash and liability outstanding without adding another shopping expense. Fees are recognized once at purchase time as `liability_fee`; paying them is included in the repayment allocation.

`GET /v1/finance/summary` now separates `recognized_spending_idr` from `cash_outflow_idr` and returns total assets, liabilities, net worth, and liability balances. `DELETE` remains a void operation; repayment allocations are reversed transactionally so financial history is retained. All mutation endpoints accept `Idempotency-Key`.

For an existing deployment, run the forward-only `002_finance_liabilities.sql` through `go run ./cmd/migrate` against a backup or Neon branch first. The migration adds columns and tables only; it does not drop finance data. Do not run integration tests against production.

## API tour

```bash
export MIMO_URL=http://127.0.0.1:8080
export MIMO_TOKEN='the_same_very_long_random_token_from_env'

curl -s -X POST "$MIMO_URL/v1/notes" \
 -H "Authorization: Bearer $MIMO_TOKEN" -H 'Content-Type: application/json' \
 -d '{"kind":"work","title":"PMP automation","content":"Build and distribute once","tags":["devops"]}'

curl -s -X POST "$MIMO_URL/v1/finance/accounts" \
 -H "Authorization: Bearer $MIMO_TOKEN" -H 'Content-Type: application/json' \
 -d '{"name":"Cash","kind":"cash","opening_balance_idr":500000}'

# Replace ACCOUNT_UUID with returned account id
curl -s -X POST "$MIMO_URL/v1/finance/transactions" \
 -H "Authorization: Bearer $MIMO_TOKEN" \
 -H 'Idempotency-Key: expense-from-wa-message-123' \
 -H 'Content-Type: application/json' \
 -d '{"type":"expense","amount_idr":28000,"from_account_id":"ACCOUNT_UUID","description":"Makan siang"}'

curl -s "$MIMO_URL/v1/finance/summary" -H "Authorization: Bearer $MIMO_TOKEN"

curl -s -X POST "$MIMO_URL/v1/search" \
 -H "Authorization: Bearer $MIMO_TOKEN" -H 'Content-Type: application/json' \
 -d '{"query":"PMP build","limit":5}'

# R2 must be configured before this upload (max default 15 MB).
curl -s -X POST "$MIMO_URL/v1/assets/upload" \
 -H "Authorization: Bearer $MIMO_TOKEN" \
 -F 'caption=Router lab di meja kerja' -F 'file=@./router.jpg'
# GET /v1/assets/{id}/download returns a private signed URL valid for 5 minutes.
```

**Finance rules:** transactions always use `amount_idr` integer (not floats). Expense uses `from_account_id`, income uses `to_account_id`, transfer uses both. Summary excludes transfers from income/expense totals. Balances = opening balance + posted incoming - posted outgoing. `DELETE` voids a posted transaction; updates snapshot the previous values. Idempotency keys prevent duplicate inserts on network retries; for chat ingestion, derive a stable idempotency key from the WhatsApp event ID, not merely from the generated text.

## OpenClaw integration

See `openclaw-plugin/README.md`. Build the TypeScript plugin, make `MIMO_API_URL` and `MIMO_API_TOKEN` available to the **Gateway process**, install as a native plugin, and approve only the Mimo tools in OpenClaw tool policy. The agent must **never** be given the Neon database password or R2 private secret directly.

## Photo and object retrieval

1. Upload a JPG/PNG/WebP/PDF with a caption to the Go API; R2 holds the original and Neon holds metadata.
2. If the photo is related to an object, create an object and attach the returned asset UUID with `POST /v1/objects/{id}/assets`.
3. Search by words in object descriptions, note contents, or photo captions (`/v1/search`).
4. Retrieve an R2 signed URL using `/v1/assets/{id}/download`.

**Not yet automatic:** extracting captions using a vision model, ingestion of inbound WhatsApp attachments, OCR, and image-to-image similarity. The Go API and R2 storage are ready for these additions but this ZIP does **not** claim that WhatsApp photos are ingested end-to-end automatically. The plugin can search/retrieve registered assets now.

## Search/indexing and semantic memory

Writes synchronously upsert `mimo.search_chunks` and become immediately searchable through PostgreSQL full-text search. Set `EMBEDDING_URL`, `EMBEDDING_MODEL=embeddinggemma-2:440m`, and `EMBEDDING_DIMENSIONS=768` to enable durable background vector indexing with `mimo-worker`. Use `http://127.0.0.1:11434/api/embed` when Go runs on the VPS host, or `http://mimo-ollama:11434/api/embed` only when the containers share `embeddings-engine_default`.

Run `go run ./cmd/migrate` to apply migration `003_embedding.sql`; it preserves the historical vector tables and adds `mimo.semantic_embeddings` with `vector(768)`. Use `POST /v1/memory/search` or the OpenClaw `mimo_memory_search` tool for semantic memory retrieval. Search is owner-scoped and only returns active personal memories. See [docs/semantic-memory.md](docs/semantic-memory.md) for backfill, operations, and rollback.

If you add embeddings after records already exist, run the owner-scoped `scripts/backfill.sql` to queue chunks. Existing CRUD does not depend on embedding availability; if Ollama fails, lexical `/v1/search` continues to work and semantic search reports an unavailable provider.

## Privacy and operations

- Single owner MVP: `MIMO_OWNER_ID` and `MIMO_API_TOKEN` are configured server-side; not supplied by LLM. **Not multi-user ready**. Every query filters by owner and asset relationship checks ownership.
- The DB user must have narrowly scoped permissions where practical. Avoid giving a production DB owner credential to the public-facing API after provisioning. Schema migrations use owner privileges.
- Uploads are type-checked and size-limited; R2 objects remain private. Signed media download links are short-lived but should still be treated as credentials.
- Soft-deleted asset rows lose API access immediately; R2 deletion is best effort. Set up orphan cleanup and backups before long-term use. Expired memory physical cleanup is also future work.
- Do not store confidential corporate screenshots, credentials, or customer data without explicit permission under company policy.
- For finance integrity, validate records and reconcile against actual bank statements. This is not bank integration and no account is synced automatically.
- Runtime: small pgx pool (API 4, worker 2). Avoid HNSW, Redis, Kafka, or Elasticsearch until metrics justify them.

## Engineering next steps

1. Harden PostgreSQL roles + optional RLS for multi-user access.
2. Add webhook/media bridge to push WhatsApp image attachments into the Go upload endpoint.
3. Add vision captioning and OCR with explicit cost/retention controls.
4. Implement stronger immutable finance ledger + reconciliation if moving from personal spending tracker to accounting.
5. Add CI integration tests using a temporary Neon branch or PostgreSQL with pgvector installed.
