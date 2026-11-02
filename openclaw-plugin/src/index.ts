import { Type } from "typebox";

// Keep this plugin thin: the Go API owns validation, data access, and permissions.
const BASE = (process.env.MIMO_API_URL || "http://127.0.0.1:8080").replace(/\/$/, "");
const TOKEN = process.env.MIMO_API_TOKEN || "";

type Args = Record<string, unknown>;
function select(args: Args, fields: string[]): Args {
  return Object.fromEntries(fields.filter((k) => args[k] !== undefined).map((k) => [k, args[k]]));
}
async function request(method: string, path: string, data?: Args, idem?: string) {
  if (!TOKEN) throw new Error("MIMO_API_TOKEN missing from OpenClaw environment");
  const headers: Record<string, string> = { Authorization: `Bearer ${TOKEN}` };
  if (data !== undefined) headers["Content-Type"] = "application/json";
  if (idem) headers["Idempotency-Key"] = idem.slice(0, 120);
  const response = await fetch(BASE + path, {
    method, headers, body: data === undefined ? undefined : JSON.stringify(data),
    signal: AbortSignal.timeout(30000),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`Mimo API HTTP ${response.status}: ${text.slice(0, 500)}`);
  try { return JSON.parse(text); } catch { return { text }; }
}
function pathID(id: unknown): string {
  if (typeof id !== "string" || !/^[a-fA-F0-9-]{36}$/.test(id)) throw new Error("A valid UUID id is required");
  return encodeURIComponent(id);
}
function result(data: unknown) {
  return { content: [{ type: "text" as const, text: JSON.stringify(data) }] };
}
const S = Type.String;
const opt = Type.Optional;
function register(api: any, name: string, description: string, parameters: any,
  run: (id: string, p: Args) => Promise<unknown>) {
  api.registerTool({
    name, description, parameters,
    async execute(toolCallId: string, params: Args) {
      return result(await run(toolCallId, params));
    },
  });
}
export default {
  id: "mimo-tools",
  name: "Mimo Tools",
  register(api: any) {
    register(api, "mimo_notes", "Create/list/read/edit/delete persistent personal notes and ideas.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("delete")]),
        id: opt(S()), title: opt(S()), content: opt(S()), kind: opt(S()), tags: opt(Type.Array(S())),
      }), async (_id, p) => {
        const action = p.action as string;
        if (action === "create") return request("POST", "/v1/notes", select(p, ["title", "content", "kind", "tags"]));
        if (action === "list") return request("GET", "/v1/notes");
        const path = `/v1/notes/${pathID(p.id)}`;
        if (action === "get") return request("GET", path);
        if (action === "update") return request("PATCH", path, select(p, ["title", "content", "kind", "tags"]));
        return request("DELETE", path);
      });
    register(api, "mimo_objects", "Maintain a searchable object inventory with descriptions and optionally link uploaded asset UUIDs.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("delete"), Type.Literal("attach_asset")]),
        id: opt(S()), name: opt(S()), description: opt(S()), category: opt(S()), asset_id: opt(S()),
      }), async (_id, p) => {
        const action = p.action as string;
        if (action === "create") return request("POST", "/v1/objects", select(p, ["name", "description", "category"]));
        if (action === "list") return request("GET", "/v1/objects");
        const path = `/v1/objects/${pathID(p.id)}`;
        if (action === "get") return request("GET", path);
        if (action === "update") return request("PATCH", path, select(p, ["name", "description", "category"]));
        if (action === "attach_asset") return request("POST", path + "/assets", select(p, ["asset_id"]));
        return request("DELETE", path);
      });
    register(api, "mimo_memory", "Store, list, and forget durable personal facts, not finance ledger entries.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("delete")]),
        id: opt(S()), title: opt(S()), summary: opt(S()), category: opt(S()), importance: opt(Type.Integer({ minimum: 1, maximum: 5 })),
      }), async (_id, p) => {
        if (p.action === "create") return request("POST", "/v1/memories", select(p, ["title", "summary", "category", "importance"]));
        if (p.action === "list") return request("GET", "/v1/memories");
        return request("DELETE", `/v1/memories/${pathID(p.id)}`);
      });
    register(api, "mimo_search", "Find stored notes, objects, memory, or photo captions; returns source IDs. Do not invent missing results.",
      Type.Object({ query: S({ minLength: 2 }), limit: opt(Type.Integer({ minimum: 1, maximum: 30 })) }),
      async (_id, p) => request("POST", "/v1/search", select(p, ["query", "limit"])));
    register(api, "mimo_finance_accounts", "Create a financial account or list accounts with database-calculated IDR balances.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("list")]), name: opt(S()), kind: opt(S()), opening_balance_idr: opt(Type.Integer()) }),
      async (_id, p) => p.action === "list" ? request("GET", "/v1/finance/accounts") : request("POST", "/v1/finance/accounts", select(p, ["name", "kind", "opening_balance_idr"])));
    register(api, "mimo_finance_categories", "Create or list income/expense categories.",
      Type.Object({ action: Type.Union([Type.Literal("create"), Type.Literal("list")]), name: opt(S()), kind: opt(S()) }),
      async (_id, p) => p.action === "list" ? request("GET", "/v1/finance/categories") : request("POST", "/v1/finance/categories", select(p, ["name", "kind"])));
    register(api, "mimo_finance_transactions", "Record/list/get/update/void IDR transactions. Account and category IDs must be valid. Transfers are NOT expenses.",
      Type.Object({
        action: Type.Union([Type.Literal("create"), Type.Literal("list"), Type.Literal("get"), Type.Literal("update"), Type.Literal("void")]),
        id: opt(S()), type: opt(S()), amount_idr: opt(Type.Integer({ minimum: 1 })),
        from_account_id: opt(S()), to_account_id: opt(S()), category_id: opt(S()),
        description: opt(S()), occurred_at: opt(S()),
      }), async (callID, p) => {
        const action = p.action as string;
        if (action === "create") return request("POST", "/v1/finance/transactions", select(p, ["type", "amount_idr", "from_account_id", "to_account_id", "category_id", "description", "occurred_at"]), callID);
        if (action === "list") return request("GET", "/v1/finance/transactions");
        const path = `/v1/finance/transactions/${pathID(p.id)}`;
        if (action === "get") return request("GET", path);
        if (action === "update") return request("PATCH", path, select(p, ["amount_idr", "category_id", "description", "occurred_at"]));
        return request("DELETE", path);
      });
    register(api, "mimo_finance_summary", "Accurate SQL-computed income, expenses, and categories by date range (to is exclusive).",
      Type.Object({ from: opt(S()), to: opt(S()) }), async (_id, p) => {
        const qs = new URLSearchParams(); if (p.from) qs.set("from", String(p.from)); if (p.to) qs.set("to", String(p.to));
        return request("GET", "/v1/finance/summary" + (qs.size ? `?${qs}` : ""));
      });
    register(api, "mimo_assets", "List image/PDF metadata, read asset details, or get a short-lived private R2 download URL. Upload separately through Go API.",
      Type.Object({ action: Type.Union([Type.Literal("list"), Type.Literal("get"), Type.Literal("download")]), id: opt(S()) }),
      async (_id, p) => {
        if (p.action === "list") return request("GET", "/v1/assets");
        const path = `/v1/assets/${pathID(p.id)}`;
        return request("GET", p.action === "download" ? path + "/download" : path);
      });
  },
};
