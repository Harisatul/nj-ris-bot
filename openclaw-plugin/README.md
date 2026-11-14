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
MIMO_API_TOKEN=<same token as Go API>
MIMO_AGENT_ISOLATED=true
```

If Go API lives in Docker on the same host, host port `127.0.0.1:8080` is mapped by compose. If Gateway lives in Docker too, use a private shared Docker network and service name instead.

The tools are declared in `openclaw.plugin.json`; check tool policies in your installed OpenClaw version and allow only `mimo-tools` or the needed tool names. The verified OpenClaw 2026.10.10 loader passes one plugin configuration/environment to the Gateway process, not separate credential environments per agent. Therefore `MIMO_AGENT_ISOLATED=true` is only safe when each agent runs in a separate Gateway/process/container with its own environment. With the default `false`, the plugin registers no tools. Verify with `openclaw plugins inspect mimo-tools --runtime` (command availability may depend on version). **Restrict WhatsApp sender allowlists to yourself** while the backend uses single-owner authentication.

`mimo_memory_search` calls `POST /v1/memory/search` and returns concise, owner-scoped active personal-memory matches with stable IDs and similarity scores. It never receives database credentials or executes SQL.

Not yet automated: ingestion of inbound WhatsApp image bytes into R2. For now upload a photo to `/v1/assets/upload` (curl), save its caption, then query/download via `mimo_assets` and `mimo_search`.
