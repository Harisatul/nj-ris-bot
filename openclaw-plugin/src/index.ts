import { Type } from "typebox";
import { createHash } from "node:crypto";
import { authorizeToolContext } from "./broker/authorization.js";
import { TrustedCredentialBroker, type MimoRequest } from "./broker/credentials.js";
import type { TrustedToolContext } from "./broker/identity.js";

// Keep this plugin thin: the Go API owns validation, data access, and permissions.
const BASE = (process.env.MIMO_API_URL || "http://127.0.0.1:8080").replace(/\/$/, "");
// This remains an explicit enable flag for compatibility. In multi-agent mode
// the broker additionally requires all three distinct agent credentials and a
// trusted OpenClaw contextVersion=2 invocation context.
const ENABLED = process.env.MIMO_AGENT_ISOLATED === "true";

type Args = Record<string, unknown>;
function select(args: Args, fields: string[]): Args {
  return Object.fromEntries(fields.filter((k) => args[k] !== undefined).map((k) => [k, args[k]]));
}
function operationKey(prefix: string, args: Args, fields: string[]): string {
  const supplied = args.idempotency_key;
  if (typeof supplied === "string" && supplied.trim()) return supplied.trim().slice(0, 120);
  return `${prefix}:${createHash("sha256").update(JSON.stringify(select(args, fields))).digest("hex").slice(0, 48)}`;
}
function pathID(id: unknown): string {
  if (typeof id !== "string" || !/^[a-fA-F0-9-]{36}$/.test(id)) throw new Error("A valid UUID id is required");
  return encodeURIComponent(id);
}
function result(data: unknown) {
  return { content: [{ type: "text" as const, text: JSON.stringify(data) }] };
}

function compactSearch(data: any) {
  if (!data || !Array.isArray(data.results)) return data;

  return {
    semantic_enabled: data.semantic_enabled,
    warning: data.warning || undefined,
    results: data.results.slice(0, 5).map((r: any) => ({
      source_type: r.source_type,
      source_id: r.source_id,
      snippet: typeof r.snippet === "string"
        ? r.snippet.slice(0, 400)
        : "",
      score: r.score,
      lexical: r.lexical,
      semantic: r.semantic,
    })),
  };
}
const S = Type.String;
const opt = Type.Optional;
function register(api: any, name: string, description: string, parameters: any,
  run: (id: string, p: Args, call: MimoRequest) => Promise<unknown>, personal = false) {
  api.registerTool({
    contextVersion: 2,
    create(ctx: TrustedToolContext) {
      const broker = new TrustedCredentialBroker(BASE);
      const identity = broker.identity(ctx);
      if (personal && identity.agentId === "nara") throw new Error("timeline tools are unavailable to nara");
      const authorized = authorizeToolContext(broker, ctx);
      return {
        name, description, parameters,
        async execute(toolCallId: string, params: Args) {
          return result(await run(toolCallId, params, authorized.request));
        },
      };
    },
  }, { name });
}
export default {
  id: "mimo-tools",
  name: "Mimo Tools",
  register(api: any) {
    if (!ENABLED) return;
    register(api, "mimo_notes", "Create/list/read/edit/delete persistent personal notes and ideas.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("delete")]),
        id: opt(S()), title: opt(S()), content: opt(S()), kind: opt(S()), tags: opt(Type.Array(S())),
      }), async (_id, p, call) => {
        const action = p.action as string;
        if (action === "create") return call("POST", "/v1/notes", select(p, ["title", "content", "kind", "tags"]));
        if (action === "list") return call("GET", "/v1/notes");
        const path = `/v1/notes/${pathID(p.id)}`;
        if (action === "get") return call("GET", path);
        if (action === "update") return call("PATCH", path, select(p, ["title", "content", "kind", "tags"]));
        return call("DELETE", path);
      });
    register(api, "mimo_objects", "Maintain a searchable object inventory with descriptions and optionally link uploaded asset UUIDs.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("delete"), Type.Literal("attach_asset")]),
        id: opt(S()), name: opt(S()), description: opt(S()), category: opt(S()), asset_id: opt(S()),
      }), async (_id, p, call) => {
        const action = p.action as string;
        if (action === "create") return call("POST", "/v1/objects", select(p, ["name", "description", "category"]));
        if (action === "list") return call("GET", "/v1/objects");
        const path = `/v1/objects/${pathID(p.id)}`;
        if (action === "get") return call("GET", path);
        if (action === "update") return call("PATCH", path, select(p, ["name", "description", "category"]));
        if (action === "attach_asset") return call("POST", path + "/assets", select(p, ["asset_id"]));
        return call("DELETE", path);
      });
    register(api, "mimo_memory", "Store, list, and forget durable personal facts, not finance ledger entries.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("delete")]),
        id: opt(S()), title: opt(S()), summary: opt(S()), category: opt(S()), importance: opt(Type.Integer({ minimum: 1, maximum: 5 })),
      }), async (_id, p, call) => {
        if (p.action === "create") return call("POST", "/v1/memories", select(p, ["title", "summary", "category", "importance"]));
        if (p.action === "list") return call("GET", "/v1/memories");
        return call("DELETE", `/v1/memories/${pathID(p.id)}`);
      });
    register(api, "mimo_memory_search", "Retrieve grounded semantic memory matches with stable memory IDs and similarity scores. Use only the returned memories; do not invent context.",
      Type.Object({ query: S({ minLength: 2, maxLength: 1000 }), limit: opt(Type.Integer({ minimum: 1, maximum: 20 })), min_similarity: opt(Type.Number({ minimum: -1, maximum: 1 })) }),
      async (_id, p, call) => call("POST", "/v1/memory/search", select(p, ["query", "limit", "min_similarity"])));
    register(api, "mimo_search", "Find stored notes, objects, memory, or photo captions; returns source IDs. Do not invent missing results.",
      Type.Object({ query: S({ minLength: 2 }), limit: opt(Type.Integer({ minimum: 1, maximum: 30 })) }),
      async (_id, p, call) => {
        const response = await call("POST", "/v1/search", {
          query: p.query,
          limit: Math.min(Number(p.limit ?? 5), 5),
        });
        return compactSearch(response);
      });
    register(api, "mimo_finance_accounts", "Create/list/get asset and liability accounts with database-calculated IDR balances and available credit.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get")]), id: opt(S()), name: opt(S()), kind: opt(S()), classification: opt(S()), opening_balance_idr: opt(Type.Integer()), credit_limit_idr: opt(Type.Integer()) }),
      async (_id, p, call) => { if (p.action === "list") return call("GET", "/v1/finance/accounts"); if (p.action === "get") return call("GET", `/v1/finance/accounts/${pathID(p.id)}`); return call("POST", "/v1/finance/accounts", select(p, ["name", "kind", "classification", "opening_balance_idr", "credit_limit_idr"])); });
    register(api, "mimo_finance_categories", "Create or list income/expense categories.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("list")]), name: opt(S()), kind: opt(S()) }),
      async (_id, p, call) => p.action === "list" ? call("GET", "/v1/finance/categories") : call("POST", "/v1/finance/categories", select(p, ["name", "kind"])));
    register(api, "mimo_finance_transactions", "Record/list/get/update/void IDR transactions. Account and category IDs must be valid. Transfers are NOT expenses.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("purchase"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("void")]),
        id: opt(S()), type: opt(S()), amount_idr: opt(Type.Integer({ minimum: 1 })),
        from_account_id: opt(S()), to_account_id: opt(S()), category_id: opt(S()),
        liability_account_id: opt(S()), principal_amount_idr: opt(Type.Integer({ minimum: 1 })), total_fee_idr: opt(Type.Integer({ minimum: 0 })), tenor: opt(Type.Integer({ minimum: 1, maximum: 120 })), first_due_date: opt(S()),
        description: opt(S()), occurred_at: opt(S()), idempotency_key: opt(S()),
      }), async (callID, p, call) => {
        const action = p.action as string;
        if (action === "create") return call("POST", "/v1/finance/transactions", select(p, ["type", "amount_idr", "from_account_id", "to_account_id", "category_id", "description", "occurred_at"]), operationKey("transaction", p, ["type", "amount_idr", "from_account_id", "to_account_id", "category_id", "description", "occurred_at"]));
        if (action === "purchase") return call("POST", "/v1/finance/liability-purchases", select(p, ["liability_account_id", "principal_amount_idr", "total_fee_idr", "tenor", "first_due_date", "category_id", "description", "occurred_at"]), operationKey("liability-purchase", p, ["liability_account_id", "principal_amount_idr", "total_fee_idr", "tenor", "first_due_date", "description"]));
        if (action === "list") return call("GET", "/v1/finance/transactions");
        const path = `/v1/finance/transactions/${pathID(p.id)}`;
        if (action === "get") return call("GET", path);
        if (action === "update") return call("PATCH", path, select(p, ["amount_idr", "category_id", "description", "occurred_at"]));
        return call("DELETE", path);
      });
    register(api, "mimo_finance_installments", "List BNPL/credit installment schedules and record atomic partial or full repayments. Never repeat a payment after a timeout without checking its idempotency key.",
      Type.Object({ action: Type.Union([Type.Literal("list"), Type.Literal("upcoming"), Type.Literal("overdue"), Type.Literal("pay")]), id: opt(S()), debt_id: opt(S()), asset_account_id: opt(S()), amount_idr: opt(Type.Integer({ minimum: 1 })), description: opt(S()), idempotency_key: opt(S()) }),
      async (_id, p, call) => { if (p.action === "pay") return call("POST", `/v1/finance/installments/${pathID(p.id)}/payments`, select(p, ["asset_account_id", "amount_idr", "description"]), operationKey("installment-payment", p, ["id", "asset_account_id", "amount_idr", "description"])); const qs = new URLSearchParams(); if (p.debt_id) qs.set("debt_id", String(p.debt_id)); if (p.action === "upcoming") qs.set("upcoming", "true"); if (p.action === "overdue") qs.set("overdue", "true"); return call("GET", "/v1/finance/installments" + (qs.size ? `?${qs}` : "")); });
    register(api, "mimo_finance_summary", "Accurate SQL-computed income, expenses, and categories by date range (to is exclusive).",
      Type.Object({ from: opt(S()), to: opt(S()) }), async (_id, p, call) => {
        const qs = new URLSearchParams(); if (p.from) qs.set("from", String(p.from)); if (p.to) qs.set("to", String(p.to));
        return call("GET", "/v1/finance/summary" + (qs.size ? `?${qs}` : ""));
      });
    register(api, "mimo_assets", "List image/PDF metadata, read asset details, or get a short-lived private R2 download URL. Upload separately through Go API.",
      Type.Object({ action: Type.Union([Type.Literal("list"), Type.Literal("get"), Type.Literal("download")]), id: opt(S()) }),
      async (_id, p, call) => {
        if (p.action === "list") return call("GET", "/v1/assets");
        const path = `/v1/assets/${pathID(p.id)}`;
        return call("GET", p.action === "download" ? path + "/download" : path);
      });
    register(api, "mimo_knowledge", "Save shared reading sources and distinct human or AI-attributed interpretations; workspace is authorization-checked by Mimo.",
      Type.Object({ action: Type.Union([Type.Literal("source"), Type.Literal("note"), Type.Literal("search")]), workspace: S(), query: opt(S()), title: opt(S()), body: opt(S()), source_id: opt(S()), source_type: opt(S()), authors: opt(Type.Array(S())), url: opt(S()), authored_by: opt(Type.Union([Type.Literal("human"), Type.Literal("ai")])) }),
      async (_id, p, call) => {
        if (p.action === "source") return call("POST", "/v1/knowledge/sources", select(p, ["workspace", "title", "source_type", "authors", "url"]));
        if (p.action === "note") return call("POST", "/v1/knowledge/notes", select(p, ["workspace", "title", "body", "source_id", "authored_by"]));
        return call("POST", "/v1/knowledge/search", select(p, ["workspace", "query"]));
      });
    register(api, "mimo_moments", "Capture or search shared moments. Mimo keeps occurred_at separate from recorded_at and applies workspace authorization.",
      Type.Object({ action: Type.Union([Type.Literal("capture"), Type.Literal("search")]), workspace: S(), title: opt(S()), description: opt(S()), occurred_at: opt(S()), query: opt(S()), tags: opt(Type.Array(S())), location: opt(S()) }),
      async (_id, p, call) => p.action === "capture" ? call("POST", "/v1/moments", select(p, ["workspace", "title", "description", "occurred_at", "tags", "location"])) : call("POST", "/v1/moments/search", select(p, ["workspace", "query"])));
    register(api, "mimo_discussion", "Store a structured discussion while keeping human perspectives and AI synthesis explicitly attributed.",
      Type.Object({ workspace: S(), title: S(), body: S(), source_id: opt(S()), perspectives: opt(Type.Array(Type.Any())), source_refs: opt(Type.Array(Type.Any())), synthesis: opt(S()), unresolved_questions: opt(Type.Array(S())), authored_by: opt(Type.Union([Type.Literal("human"), Type.Literal("ai")])) }),
      async (_id, p, call) => call("POST", "/v1/knowledge/discussions", p));
    register(api, "mimo_legacy", "Create explicitly curated legacy collections. Adding source material never grants redistribution rights.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("approve")]), workspace: S(), id: opt(S()), slug: opt(S()), title: opt(S()), description: opt(S()) }),
      async (_id, p, call) => p.action === "approve" ? call("POST", `/v1/legacy/collections/${pathID(p.id)}/approve?workspace=${encodeURIComponent(String(p.workspace))}`) : call("POST", "/v1/legacy/collections", select(p, ["workspace", "slug", "title", "description"])));
    register(api, "mimo_share", "Grant or revoke workspace membership; Mimo requires an authenticated human workspace owner.",
      Type.Object({ action: Type.Union([Type.Literal("grant"), Type.Literal("revoke")]), workspace: S(), principal: S(), role: opt(S()), permission: opt(S()) }),
      async (_id, p, call) => call("POST", "/v1/sharing", select(p, ["action", "workspace", "principal", "role", "permission"])));
    register(api, "mimo_activity_log", "Record personal activities atomically; use RFC3339 timestamps and omit ended_at for in-progress work.",
      Type.Object({ activities: Type.Array(Type.Object({ title: S(), description: opt(S()), category: opt(S()), tags: opt(Type.Array(S())), started_at: S(), ended_at: opt(S()), event_type: opt(S()), time_precision: opt(S()), source_kind: opt(S()), source_message_id: opt(S()) })), idempotency_key: opt(S()) }),
      async (_id, p, call) => call("POST", "/v1/activities/batch", select(p, ["activities"]), operationKey("activity-batch", p, ["activities"])), true);
    register(api, "mimo_activity_query", "Query the complete personal activity timeline with date filters and pagination.",
      Type.Object({ from: opt(S()), to: opt(S()), category: opt(S()), q: opt(S()), status: opt(S()), limit: opt(Type.Integer({ minimum: 1, maximum: 200 })), offset: opt(Type.Integer({ minimum: 0 })), sort: opt(Type.Union([Type.Literal("asc"), Type.Literal("desc")])) }),
      async (_id, p, call) => { const q = new URLSearchParams(); for (const k of ["from", "to", "category", "q", "status", "limit", "offset", "sort"]) if (p[k] !== undefined) q.set(k, String(p[k])); return call("GET", "/v1/activities" + (q.size ? `?${q}` : "")); }, true);
    register(api, "mimo_activity_correct", "Correct a personal activity by UUID and current optimistic-concurrency version.",
      Type.Object({ id: S(), version: Type.Integer({ minimum: 1 }), title: S(), description: opt(S()), category: opt(S()), tags: opt(Type.Array(S())), started_at: S(), ended_at: opt(S()), idempotency_key: opt(S()) }),
      async (_id, p, call) => call("PATCH", `/v1/activities/${pathID(p.id)}`, select(p, ["version", "title", "description", "category", "tags", "started_at", "ended_at"]), operationKey("activity-correct", p, ["id", "version", "title", "description", "category", "tags", "started_at", "ended_at"])), true);
    register(api, "mimo_activity_void", "Void a personal activity without deleting its audit history.",
      Type.Object({ id: S(), version: Type.Integer({ minimum: 1 }), idempotency_key: opt(S()) }),
      async (_id, p, call) => call("POST", `/v1/activities/${pathID(p.id)}/void`, select(p, ["version"]), operationKey("activity-void", p, ["id", "version"])), true);
    register(api, "mimo_timeline_summary", "Return deterministic personal timeline rows or hourly unique coverage; unknown time remains unknown.",
      Type.Object({ mode: opt(Type.Union([Type.Literal("timeline"), Type.Literal("coverage")])), from: opt(S()), to: opt(S()), limit: opt(Type.Integer({ minimum: 1, maximum: 200 })), offset: opt(Type.Integer({ minimum: 0 })) }),
      async (_id, p, call) => { const q = new URLSearchParams(); for (const k of ["from", "to", "limit", "offset"]) if (p[k] !== undefined) q.set(k, String(p[k])); return call("GET", (p.mode === "coverage" ? "/v1/timeline/coverage" : "/v1/timeline") + (q.size ? `?${q}` : "")); }, true);
    register(api, "mimo_habit_manage", "Create, list, or update approved personal habits.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("update")]), id: opt(S()), name: opt(S()), metric: opt(Type.Union([Type.Literal("frequency"), Type.Literal("duration"), Type.Literal("completion")])), target: opt(Type.Number({ minimum: 0.01 })), period: opt(Type.Union([Type.Literal("day"), Type.Literal("week")])), category: opt(S()), active: opt(Type.Boolean()) }),
      async (_id, p, call) => { if (p.action === "list") return call("GET", "/v1/habits"); if (p.action === "update") return call("PATCH", `/v1/habits/${pathID(p.id)}`, select(p, ["name", "metric", "target", "period", "category", "active"])); return call("POST", "/v1/habits", select(p, ["name", "metric", "target", "period", "category", "active"])); }, true);
    register(api, "mimo_habit_progress", "Calculate deterministic personal habit progress for a local day or week.",
      Type.Object({ date: opt(S()) }), async (_id, p, call) => { const q = p.date ? `?date=${encodeURIComponent(String(p.date))}` : ""; return call("GET", "/v1/habits/progress" + q); }, true);
    register(api, "mimo_checkin_settings", "Read or update opt-in adaptive check-in settings; this does not send WhatsApp messages.",
      Type.Object({ action: Type.Union([Type.Literal("get"), Type.Literal("update")]), enabled: opt(Type.Boolean()), strategy: opt(Type.Literal("adaptive")), gap_threshold_minutes: opt(Type.Integer({ minimum: 15, maximum: 1440 })), interval_minutes: opt(Type.Integer({ minimum: 15, maximum: 1440 })), cooldown_minutes: opt(Type.Integer({ minimum: 0, maximum: 1440 })), max_per_day: opt(Type.Integer({ minimum: 0, maximum: 24 })), quiet_start: opt(S()), quiet_end: opt(S()), timezone: opt(S()), pause_after_unanswered: opt(Type.Integer({ minimum: 1, maximum: 20 })), snoozed_until: opt(S()) }),
      async (_id, p, call) => p.action === "get" ? call("GET", "/v1/checkin/settings") : call("PATCH", "/v1/checkin/settings", select(p, ["enabled", "strategy", "gap_threshold_minutes", "interval_minutes", "cooldown_minutes", "max_per_day", "quiet_start", "quiet_end", "timezone", "pause_after_unanswered", "snoozed_until"])), true);
  },
};
