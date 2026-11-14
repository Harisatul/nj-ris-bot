# Mimo v0.3 architecture

Mimo remains a Go/pgx API with PostgreSQL as the source of truth, a durable embedding queue, Ollama-compatible embeddings, private R2 objects, and the existing OpenClaw adapter. Migration `004_multiuser_knowledge_moments.sql` adds principals, workspaces, memberships, hashed credentials, permissions, knowledge, moments, and explicitly curated legacy collections without rewriting migrations 001–003.

Personal records remain in the existing tables for compatibility. New shared records are keyed by a workspace UUID and are only returned after membership and permission checks. Semantic indexing is additive and should use workspace-scoped chunks for new content; finance remains deterministic SQL.

The API has no production deployment side effect. Apply migrations against a backup or Neon branch first, then restart services during a planned maintenance window.
