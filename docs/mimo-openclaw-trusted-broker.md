# Mimo trusted credential broker audit

Audited against the installed OpenClaw package `2026.10.10` (`7dc3350`) in `openclaw-plugin/node_modules/openclaw`.

## Capability finding

The installed SDK exposes `api.registerTool` overloads for `OpenClawPluginToolFactory` and `OpenClawPluginToolFactory<2>`. Version 2 is registered as:

```ts
api.registerTool({
  contextVersion: 2,
  create(ctx) { /* return the tool */ }
})
```

The runtime context includes host-selected `agentId`, `sessionKey`, optional `sessionId`, and `assertInvocationCurrent`. The host factory wrapper also checks plugin lifecycle authority and rechecks invocation authority around `execute`. The broker calls the same authority before the HTTP request and after the response.

The model/tool argument schema has no credential or agent selector. Prompt text and forged arguments cannot change the selected token. Unsupported or missing runtime identity fails closed.

## Gateway boundary

Plugin configuration and environment are Gateway-process scoped, not isolated per agent. The broker therefore does not provide OS-level secret isolation. A malicious native plugin, local Gateway operator, or agent with arbitrary shell/environment access may still read all three environment variables. Keep shell access restricted and treat native plugins as trusted code. For a stronger threat model, use separate tool runner processes or an authenticated external broker.

The existing `MIMO_AGENT_ISOLATED=true` gate remains required to register tools. It is an enable gate, not a claim of process isolation. With `MIMO_TRUSTED_BROKER=true`, all three distinct credentials are required. Without that flag, compatibility mode accepts only `MIMO_API_TOKEN` for Neura; Najwa and Nara fail closed rather than inheriting the legacy token.

## Session switching

The selected agent identity is captured from the runtime factory context, while `assertInvocationCurrent` is retained by the request closure. A stale or switched invocation is rejected before sending the request and before returning its response. Retaining a tool object cannot bypass the host authority check.

## Verification

```bash
cd openclaw-plugin
npm test
```

The verification also loaded the built plugin with the installed runtime shape: `MIMO_AGENT_ISOLATED=false` registered zero tools, while `MIMO_AGENT_ISOLATED=true` registered 16 `contextVersion: 2` factories. The repository test suite covers Neura/Najwa/Nara token selection, missing and forged identity, stale invocation/session rejection, distinct credentials, and legacy Neura-only compatibility. Backend Mimo authorization remains mandatory for owner/workspace isolation and credential revocation.
