# v0.3 migration and rollback

1. Back up the database or create a Neon branch. Do not use the production `DATABASE_URL` for local tests.
2. Run `docker compose run --rm --no-deps api mimo-migrate status` and confirm migration 004 is applied.
3. Run `docker compose run --rm --no-deps api mimo-migrate migrate`; this applies additive migration 005.
4. In one administrative transaction, provision the exact personal owner mappings. For the existing data, Haris's historical owner is `me`:

   ```sql
   INSERT INTO mimo.personal_workspace_owners(workspace_id, owner_id, human_principal_id)
   SELECT w.id, 'me', h.id FROM mimo.workspaces w
   JOIN mimo.principals h ON h.slug='haris' AND h.kind='human'
   WHERE w.slug='haris-personal' AND w.kind='personal'
   ON CONFLICT DO NOTHING;
   INSERT INTO mimo.personal_workspace_owners(workspace_id, owner_id, human_principal_id)
   SELECT w.id, 'najwa', h.id FROM mimo.workspaces w
   JOIN mimo.principals h ON h.slug='najwa' AND h.kind='human'
   WHERE w.slug='najwa-personal' AND w.kind='personal'
   ON CONFLICT DO NOTHING;
   ```

   Verify each row has a personal workspace, a human owner membership with role `owner`, and no duplicate owner ID.
5. Provision each credential with a non-empty `owner_id` and the matching `default_workspace_id`. Neura must use `me` plus Haris's personal workspace; Najwa-agent must use `najwa` plus Najwa's personal workspace. Do not configure Nara with a personal workspace. Never record raw tokens in SQL history.
6. Verify cross-owner reads/writes, direct resource IDs, semantic search, revoked memberships, revoked credentials, and the legacy token before enabling agents.

Rollback is additive: stop the worker, deploy the previous binary, and leave migrations 004/005 in place. The previous binary ignores the new mapping table. To disable the hotfix safely, revoke the affected agent credentials; do not delete mappings, migration history, or the schema. Restore the database only if an application-level rollback is insufficient. Existing migration integration tests require a disposable local PostgreSQL/pgvector database via `MIMO_MIGRATION_TEST_DATABASE_URL`.
