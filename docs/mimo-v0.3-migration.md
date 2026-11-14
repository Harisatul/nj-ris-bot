# v0.3 migration and rollback

1. Back up the database or create a Neon branch.
2. Run `docker compose run --rm --no-deps api mimo-migrate status`.
3. Run `docker compose run --rm --no-deps api mimo-migrate migrate`.
4. Provision principals, workspaces, memberships, permissions, and hashed credential rows using an administrative SQL session. Never record raw tokens in SQL history.
5. Verify cross-workspace reads and revoked credentials before enabling agents.

Rollback is additive: stop the worker, deploy the previous binary, and leave migration 004 in place. The previous binary ignores new tables. Restore the database only if an application-level rollback is insufficient; do not delete migration history or drop the schema. Existing migration integration tests require a disposable local PostgreSQL/pgvector database via `MIMO_MIGRATION_TEST_DATABASE_URL`.
