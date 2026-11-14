# Mimo multi-user security

An agent credential identifies a service agent; it does not identify a human. `agent_credentials.token_hash` stores SHA-256 hashes, and credentials support revocation and expiry. Provision tokens out of band and never put them in source, prompts, or logs.

Create human principals `haris` and `najwa`, agent principals `neura`, `najwa-agent`, and `nara`, personal workspaces, and a shared workspace. Give Neura only Haris permissions, Najwa-agent only Najwa permissions, and Nara only shared knowledge/moment/legacy permissions. For a verifiable human sender, OpenClaw must pass a server-authenticated identity assertion; an arbitrary sender field is never accepted as authorship. Without that bridge, records are attributed to the service agent.

The legacy `MIMO_API_TOKEN` fallback is single-owner compatibility only and must be removed after agent credentials are provisioned. Do not expose the database or a shared credential to agents. PostgreSQL RLS is not enabled in this migration because the current pooled connection setup has no safe transaction-scoped tenant context; centralized authorization is the enforced boundary and RLS is a follow-up.

Finance, object, media, note, and memory handlers now derive ownership from the authenticated request context. Personal route permissions are checked before handler execution, and Nara has no personal permissions. The legacy token remains a single-owner compatibility fallback and must not be shared with another agent.
