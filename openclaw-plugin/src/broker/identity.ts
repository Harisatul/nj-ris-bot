export type AgentId = "neura" | "najwa" | "nara";

export interface TrustedToolContext {
  readonly agentId?: string;
  readonly sessionKey?: string;
  readonly sessionId?: string;
  readonly assertInvocationCurrent?: () => void;
}

export interface TrustedIdentity {
  readonly agentId: AgentId;
  readonly sessionKey: string;
  readonly sessionId?: string;
  readonly assertCurrent: () => void;
}

const AGENTS = new Set<AgentId>(["neura", "najwa", "nara"]);

export function resolveTrustedIdentity(ctx: TrustedToolContext): TrustedIdentity {
  if (!ctx || typeof ctx.agentId !== "string" || !AGENTS.has(ctx.agentId as AgentId)) {
    throw new Error("Mimo credential broker requires a trusted OpenClaw agent context");
  }
  if (typeof ctx.sessionKey !== "string" || ctx.sessionKey.trim() === "") {
    throw new Error("Mimo credential broker requires a bound OpenClaw session");
  }
  if (typeof ctx.assertInvocationCurrent !== "function") {
    throw new Error("Mimo credential broker requires live invocation authority");
  }

  const assertCurrent = () => ctx.assertInvocationCurrent!();
  assertCurrent();
  return {
    agentId: ctx.agentId as AgentId,
    sessionKey: ctx.sessionKey,
    sessionId: typeof ctx.sessionId === "string" ? ctx.sessionId : undefined,
    assertCurrent,
  };
}
