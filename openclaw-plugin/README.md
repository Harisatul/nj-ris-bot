# OpenClaw integration — Mimo Tools

This is a **thin adapter**. The Go API owns records, search, validation, and owner identity. Do not expose SQL execution or broad database credentials to the agent.

OpenClaw is a fast-changing project. Plugin metadata follows the native plugin documentation current on 2026-11-02; validate it against **your installed** Gateway version (`openclaw --version`) before enabling it.

```bash
cd openclaw-plugin
npm install
npm run build
openclaw plugins install --link .
openclaw plugins list
```

Make these variables available to the **OpenClaw Gateway process** (for example, in its systemd user service environment; don't place secrets in prompts):

```bash
MIMO_API_URL=http://127.0.0.1:8080
MIMO_NEURA_API_TOKEN=<Neura Mimo credential>
MIMO_NAJWA_API_TOKEN=<Najwa Mimo credential>
MIMO_NARA_API_TOKEN=<Nara Mimo credential>
MIMO_TRUSTED_BROKER=true
MIMO_AGENT_ISOLATED=true
```

If Go API lives in Docker on the same host, host port `127.0.0.1:8080` is mapped by compose. If Gateway lives in Docker too, use a private shared Docker network and service name instead.

The installed OpenClaw 2026.10.10 SDK supports a per-invocation tool factory with trusted `agentId` and `assertInvocationCurrent` context. The plugin uses `contextVersion: 2`; it never accepts agent or credential selection in tool parameters. `MIMO_AGENT_ISOLATED=false` registers no tools. `MIMO_AGENT_ISOLATED=true` is only an enable gate, not an OS/process isolation boundary: all three secrets remain Gateway-global. Review shell/exec policy, native plugin trust, filesystem permissions, and sender allowlists before enabling writes. Verify with `openclaw plugins inspect mimo-tools --runtime` and the runtime version installed on the Gateway.

`mimo_memory_search` calls `POST /v1/memory/search` and returns concise, owner-scoped active personal-memory matches with stable IDs and similarity scores. It never receives database credentials or executes SQL.

Not yet automated: ingestion of inbound WhatsApp image bytes into R2. For now upload a photo to `/v1/assets/upload` (curl), save its caption, then query/download via `mimo_assets` and `mimo_search`.
