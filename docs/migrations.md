# Production migration operations

The migration runner owns `mimo.schema_migrations`. Each row contains the migration filename, the exact SHA-256 checksum of the checked-in SQL, the applied timestamp, and whether it was a legacy baseline. The runner takes a PostgreSQL advisory lock, so only one migration process can execute at a time.

Run these commands from the application image or repository directory. Use the direct Neon connection for migrations, and do not run them against production until the same command has been verified against a Neon branch or disposable database.

```bash
# Inspect history and pending files; also detects checksum drift.
docker compose run --rm --no-deps api mimo-migrate status

# Normal install or upgrade. Already recorded files are skipped.
docker compose run --rm --no-deps api mimo-migrate migrate

# Legacy-only operation. It validates the expected 001/002 objects,
# columns, and constraints before recording their checksums as baselined.
docker compose run --rm --no-deps api mimo-migrate baseline
```

The default command is also `mimo-migrate migrate`. On a legacy database with no migration history, `migrate` automatically performs the same validation for `001_init.sql` and `002_finance_liabilities.sql`, records them as baselined, and then applies pending migrations such as `003_embedding.sql`. If validation is incomplete, it does not baseline that file; it applies it only as a normal pending migration, inside a transaction. The historical SQL files are never rewritten or used as an escape hatch.

If a migration fails, the transaction for that migration is rolled back and its filename is not recorded. Read the error, correct the deployment or database prerequisite, then rerun `status` and `migrate`. Never delete rows from `schema_migrations`, drop production schemas, or edit an already-recorded migration. A checksum mismatch means the checked-in historical SQL differs from what the database recorded: restore the original file or add a new forward migration.

For recovery, stop the migration command only after it returns, verify the database on a branch, correct the cause, and rerun. Do not restart API/worker services as part of migration recovery unless the deployment operator separately approves it.
