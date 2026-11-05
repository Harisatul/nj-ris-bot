# Before using this project for real personal data

- [ ] Fill `.env` on the **server only** and generate a random 32+ byte token (`openssl rand -hex 32`).
- [ ] Verify `go mod tidy && go test ./... && go vet ./...` on VM with network access.
- [ ] `docker compose build` and run migration first against a *test Neon branch*.
- [ ] Confirm `vector` and `pg_trgm` extensions are available; check `SELECT * FROM pg_extension`.
- [ ] Confirm API binds localhost and WhatsApp sender allowlist only allows yourself.
- [ ] Smoke test notes CRUD, objects CRUD, account setup, expense and transfer, duplicate idempotency key, finance summary.
- [ ] Upload one test photo to R2 with a caption, search it, fetch a 5-minute signed URL, then delete.
- [ ] Set `EMBEDDING_URL` to the existing Ollama `/api/embed` endpoint with `embeddinggemma-2:440m` and `EMBEDDING_DIMENSIONS=768`; verify worker creates rows in `mimo.semantic_embeddings`.
- [ ] Install OpenClaw plugin from `openclaw-plugin/` and validate its exact SDK compatibility with your installed Gateway.
- [ ] Set up DB backups / Neon restore procedures and R2 retention/lifecycle cleanup before putting valuable notes or transaction records in it.

Known MVP limitations: no automatic WhatsApp media ingestion/vision captioning, no real-time bank sync, no general OAuth/multiuser auth, no hard-delete R2 reconciliation worker, no full integration test in the authoring container (dependency download network unavailable).
