import type { AgentId, TrustedIdentity, TrustedToolContext } from "./identity.js";
import { resolveTrustedIdentity } from "./identity.js";

type Args = Record<string, unknown>;
type Method = "GET" | "POST" | "PATCH" | "DELETE";

export type MimoRequest = (method: Method, path: string, data?: Args, idempotencyKey?: string) => Promise<unknown>;

function requiredSecret(env: NodeJS.ProcessEnv, name: string): string {
  const value = env[name];
  if (!value || value.length < 24) throw new Error(`Mimo broker credential ${name} is not configured`);
  return value;
}

function loadCredentials(env: NodeJS.ProcessEnv): Readonly<Record<AgentId, string | undefined>> {
  if (env.MIMO_TRUSTED_BROKER === "true") {
    const credentials = {
      neura: requiredSecret(env, "MIMO_NEURA_API_TOKEN"),
      najwa: requiredSecret(env, "MIMO_NAJWA_API_TOKEN"),
      nara: requiredSecret(env, "MIMO_NARA_API_TOKEN"),
    } satisfies Record<AgentId, string>;
    if (new Set(Object.values(credentials)).size !== 3) {
      throw new Error("Mimo broker credentials must be distinct");
    }
    return credentials;
  }

  // Compatibility mode is deliberately single-agent: it preserves the old
  // Neura deployment without giving that token to Najwa or Nara.
  return { neura: requiredSecret(env, "MIMO_API_TOKEN"), najwa: undefined, nara: undefined };
}

export class TrustedCredentialBroker {
  private readonly credentials: Readonly<Record<AgentId, string | undefined>>;

  constructor(
    private readonly baseURL: string,
    env: NodeJS.ProcessEnv = process.env,
    private readonly fetchImpl: typeof fetch = fetch,
  ) {
    this.credentials = loadCredentials(env);
  }

  identity(ctx: TrustedToolContext): TrustedIdentity {
    return resolveTrustedIdentity(ctx);
  }

  async request(ctx: TrustedToolContext, method: Method, path: string, data?: Args, idem?: string): Promise<unknown> {
    const identity = this.identity(ctx);
    const token = this.credentials[identity.agentId];
    if (!token) throw new Error(`Mimo credential is not configured for agent ${identity.agentId}`);
    identity.assertCurrent();

    const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
    if (data !== undefined) headers["Content-Type"] = "application/json";
    if (idem) headers["Idempotency-Key"] = idem.slice(0, 120);

    const response = await this.fetchImpl(this.baseURL + path, {
      method,
      headers,
      body: data === undefined ? undefined : JSON.stringify(data),
      signal: AbortSignal.timeout(30000),
    });
    const text = await response.text();
    identity.assertCurrent();
    if (!response.ok) throw new Error(`Mimo API HTTP ${response.status}: ${text.slice(0, 500)}`);
    try { return JSON.parse(text); } catch { return { text }; }
  }
}
