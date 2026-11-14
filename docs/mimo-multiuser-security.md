# Mimo multi-user security

An agent credential identifies a service agent; it does not identify a human. `agent_credentials.token_hash` stores SHA-256 hashes, and credentials support revocation and expiry. Provision tokens out of band and never put them in source, prompts, or logs.

Create human principals `haris` and `najwa`, agent principals `neura`, `najwa-agent`, and `nara`, personal workspaces, and a shared workspace. Give Neura only Haris permissions, Najwa-agent only Najwa permissions, and Nara only shared knowledge/moment/legacy permissions. For a verifiable human sender, OpenClaw must pass a server-authenticated identity assertion; an arbitrary sender field is never accepted as authorship. Without that bridge, records are attributed to the service agent.

Migration 005 adds `mimo.personal_workspace_owners`. Personal authorization is fail-closed and requires the credential's non-empty `owner_id` and `default_workspace_id` to match one mapping, an active human owner membership, an active agent membership, and the requested permission on that same workspace. The agent slug is never used as an owner fallback. Missing mappings, mismatches, revoked memberships, and expired/revoked credentials deny access. Nara is explicitly denied personal routes even if an operator accidentally grants it a personal permission.

The legacy `MIMO_API_TOKEN` fallback is single-owner compatibility only and must be removed after agent credentials are provisioned. Do not expose the database or a shared credential to agents. PostgreSQL RLS is not enabled in this migration because the current pooled connection setup has no safe transaction-scoped tenant context; centralized authorization is the enforced boundary and RLS is a follow-up.

Finance, object, media, note, and memory handlers now derive ownership from the authenticated request context. Personal route permissions are checked before handler execution, and Nara has no personal access. The legacy token remains a single-owner compatibility fallback using `MIMO_OWNER_ID` only; it does not gain multi-user credential privileges and must not be shared with another agent.
