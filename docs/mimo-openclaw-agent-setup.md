# OpenClaw agent setup

The plugin remains one `mimo-tools` plugin and can serve one Gateway with one WhatsApp session. OpenClaw 2026.10.10 provides a trusted per-invocation tool factory context (`contextVersion: 2`) containing the host-selected `agentId` and live invocation authority. The broker uses that context, never tool arguments, to select a credential and rechecks authority around each request.

Configure the same Gateway process with three distinct secrets:

* `MIMO_NEURA_API_TOKEN=<Neura credential>`
* `MIMO_NAJWA_API_TOKEN=<Najwa credential>`
* `MIMO_NARA_API_TOKEN=<Nara credential>`
* `MIMO_TRUSTED_BROKER=true`
* `MIMO_AGENT_ISOLATED=true` (existing explicit tool-enable gate)

The broker mapping is fixed: `neura` selects Neura, `najwa` selects Najwa, and `nara` selects Nara. Missing, forged, unsupported, or stale runtime context fails closed. The backend remains the final authorization boundary and must continue to reject cross-owner requests and revoked credentials.

This is not strong process isolation. All three secrets exist in the Gateway process environment and can potentially be read by a malicious native plugin, a local operator, or an agent with arbitrary shell/environment access. Do not enable arbitrary shell access for these agents and do not claim complete credential isolation unless the Gateway's execution policy and filesystem/process controls have also been independently audited. If that threat model is unacceptable, run separate tool runners or put a credential broker behind an authenticated service boundary.

For rollback to the historical single-agent deployment, remove `MIMO_TRUSTED_BROKER`, keep only `MIMO_API_TOKEN` for Neura, and disable the other agent routes/tools; do not reuse the legacy token for Najwa or Nara.
