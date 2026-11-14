# OpenClaw agent setup

The plugin is still one `mimo-tools` plugin, but credentials must be isolated in each Gateway execution context. The verified OpenClaw 2026.10.10 runtime loads the plugin in the Gateway process and exposes environment/configuration globally; it does not provide a safe per-agent environment boundary. Configure separate Gateway processes/containers and separate environments:

* Neura: `MIMO_API_TOKEN=<Neura token>`, `MIMO_AGENT_ISOLATED=true`; personal workspace/owner Haris only.
* Najwa: `MIMO_API_TOKEN=<Najwa token>`, `MIMO_AGENT_ISOLATED=true`; personal workspace/owner Najwa only.
* Nara: `MIMO_API_TOKEN=<Nara token>`, `MIMO_AGENT_ISOLATED=true`; shared workspace knowledge, moments, and curated legacy only.

The plugin reads environment variables from the Gateway process and intentionally registers no tools unless `MIMO_AGENT_ISOLATED=true`. Validate process separation, tool policy, and sender allowlists before enabling writes.
