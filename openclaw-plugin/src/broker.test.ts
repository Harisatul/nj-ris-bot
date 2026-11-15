import assert from "node:assert/strict";
import test from "node:test";
import { authorizeToolContext } from "./broker/authorization.js";
import { TrustedCredentialBroker } from "./broker/credentials.js";

const env = {
  MIMO_TRUSTED_BROKER: "true",
  MIMO_NEURA_API_TOKEN: "neura-test-token-which-is-long-enough",
  MIMO_NAJWA_API_TOKEN: "najwa-test-token-which-is-long-enough",
  MIMO_NARA_API_TOKEN: "nara-test-token-which-is-long-enough",
} as NodeJS.ProcessEnv;

function response(body = "{\"ok\":true}") {
  return { ok: true, status: 200, text: async () => body } as Response;
}

test("selects only the credential for the trusted runtime agent", async () => {
  const seen: string[] = [];
  const broker = new TrustedCredentialBroker("http://mimo.test", env, async (_input, init) => {
    seen.push(String(init?.headers && (init.headers as Record<string, string>).Authorization));
    return response();
  });
  const context = { agentId: "nara", sessionKey: "agent:nara:whatsapp:group", assertInvocationCurrent() {} };
  const authorized = authorizeToolContext(broker, context);
  await authorized.request("GET", "/v1/knowledge/search");
  assert.equal(seen[0], `Bearer ${env.MIMO_NARA_API_TOKEN}`);
});

test("rejects missing or forged runtime identity before fetch", async () => {
  let calls = 0;
  const broker = new TrustedCredentialBroker("http://mimo.test", env, async () => {
    calls++;
    return response();
  });
  assert.throws(() => authorizeToolContext(broker, { sessionKey: "s", assertInvocationCurrent() {} }), /trusted OpenClaw agent context/);
  assert.throws(() => authorizeToolContext(broker, { agentId: "haris", sessionKey: "s", assertInvocationCurrent() {} }), /trusted OpenClaw agent context/);
  assert.equal(calls, 0);
});

test("rejects stale or switched sessions before returning a response", async () => {
  let current = true;
  const broker = new TrustedCredentialBroker("http://mimo.test", env, async () => response());
  const authorized = authorizeToolContext(broker, {
    agentId: "neura",
    sessionKey: "agent:neura:whatsapp:direct",
    assertInvocationCurrent() {
      if (!current) throw new Error("stale invocation");
    },
  });
  current = false;
  await assert.rejects(authorized.request("GET", "/v1/finance/accounts"), /stale invocation/);
});

test("requires all three distinct credentials", () => {
  assert.throws(() => new TrustedCredentialBroker("http://mimo.test", {
    ...env,
    MIMO_NARA_API_TOKEN: env.MIMO_NEURA_API_TOKEN,
  }), /credentials must be distinct/);
});

test("legacy mode remains Neura-only", async () => {
  const legacyEnv = { MIMO_API_TOKEN: env.MIMO_NEURA_API_TOKEN } as NodeJS.ProcessEnv;
  const broker = new TrustedCredentialBroker("http://mimo.test", legacyEnv, async (_input, init) => {
    assert.equal((init?.headers as Record<string, string>).Authorization, `Bearer ${env.MIMO_NEURA_API_TOKEN}`);
    return response();
  });
  await broker.request({ agentId: "neura", sessionKey: "legacy", assertInvocationCurrent() {} }, "GET", "/v1/finance/accounts");
  await assert.rejects(broker.request({ agentId: "nara", sessionKey: "legacy", assertInvocationCurrent() {} }, "GET", "/v1/knowledge/search"), /not configured for agent nara/);
});
