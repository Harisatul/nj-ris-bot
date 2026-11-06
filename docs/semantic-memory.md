# Semantic memory with EmbeddingGemma 2 440M

Mimo keeps canonical memories and notes in PostgreSQL. Their searchable text is copied to `mimo.search_chunks`; the existing `mimo.ingestion_jobs` table provides durable, retryable work. The worker calls the separately managed Ollama service and stores current 768-dimensional vectors in `mimo.semantic_embeddings`.

The older `mimo.memory_embeddings` and `mimo.search_embeddings` tables are not altered or dropped. `semantic_embeddings` is model-specific and has a unique `(chunk_id, model_id)` row, a SHA-256 content hash, and a foreign key to `search_chunks`. A vector is only eligible for retrieval when its hash matches the current chunk hash.

## Configuration

```env
EMBEDDING_URL=http://127.0.0.1:11434/api/embed
EMBEDDING_MODEL=embeddinggemma-2:440m
EMBEDDING_DIMENSIONS=768
```

On the VPS host, use `127.0.0.1`. From a container, use the Ollama hostname only when the API and worker containers are attached to the same Docker network, for example `http://mimo-ollama:11434/api/embed` on `embeddings-engine_default`. Connect the running Mimo containers to that existing network according to your deployment system; do not add a second Ollama service to this compose file. Mimo does not install Ollama or download model weights.

## Migration and backfill

Run the normal migration command against a Neon branch or backup first:

```bash
go run ./cmd/migrate
```

Migration `003_embedding.sql` adds content hashes and a new `vector(768)` table. It leaves existing vectors untouched. After the migration, build the image and use the production-safe CLI:

```bash
docker compose build
docker compose run --rm --no-deps api mimo-backfill --dry-run
docker compose run --rm --no-deps api mimo-backfill
```

The CLI reads `MIMO_OWNER_ID` and `EMBEDDING_MODEL`, synchronizes active personal memories and non-deleted personal notes, removes stale search chunks, and queues only missing or changed vectors. A completed job with the same content hash and an existing vector is preserved. Use `--retry-failed` only when you intentionally want another bounded retry cycle:

```bash
docker compose run --rm --no-deps api mimo-backfill --retry-failed
```

The command reports enqueue status, not vector-generation completion. Monitor the worker logs and verify `mimo.semantic_embeddings` separately. The SQL file in `scripts/backfill.sql` remains a psql fallback; it requires explicit `owner_id` and `model_id` variables and should not be edited with private data.

## Retrieval

`POST /v1/memory/search` accepts `{ "query": "...", "limit": 5 }` and requires the normal Bearer token. Results are limited to active personal memories owned by the configured owner, include stable IDs and similarity scores, and return a bounded snippet. `/v1/search` also uses the new semantic table for hybrid lexical/vector results. Finance remains deterministic SQL and never uses semantic retrieval.

The OpenClaw plugin exposes `mimo_memory_search`. If the embedding service is unavailable, ordinary CRUD and lexical `/v1/search` continue to work; the dedicated semantic route returns `503`.

## Operations and rollback

The worker retries transient Ollama failures three times per request and records durable job failures with exponential backoff. It uses `SKIP LOCKED`, a two-connection pool, and stale-job recovery. Content hashes prevent stale jobs from overwriting newer text. Deleting a note or memory deletes its chunk and vector through cascading foreign keys.

Useful verification queries:

```sql
SELECT status, count(*) FROM mimo.ingestion_jobs
WHERE model_id = 'embeddinggemma-2:440m' GROUP BY status ORDER BY status;

SELECT count(*) AS vectors
FROM mimo.semantic_embeddings
WHERE model_id = 'embeddinggemma-2:440m' AND dimensions = 768;

SELECT count(*) AS stale_vectors
FROM mimo.semantic_embeddings e
JOIN mimo.search_chunks c ON c.id=e.chunk_id
WHERE e.content_hash IS DISTINCT FROM c.content_hash;
```

For rollback, stop the worker, deploy the previous Mimo binary, and leave migration 003 in place. The new tables are additive and unused by the previous binary; do not drop them while any newer binary may run. Re-enable the worker after correcting the Ollama URL/model or database migration issue.

## Tests and resource considerations

Use a mocked HTTP server for embedding-client tests and a disposable PostgreSQL/pgvector database for migration and retrieval integration tests. No ANN index is created initially: exact cosine search is appropriate for a small personal corpus and avoids index build cost. Revisit an HNSW index only after measuring corpus size and query latency.
