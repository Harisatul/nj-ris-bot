import type { TrustedToolContext } from "./identity.js";
import type { MimoRequest, TrustedCredentialBroker } from "./credentials.js";

export interface BrokerAuthorization {
  readonly request: MimoRequest;
}

export function authorizeToolContext(broker: TrustedCredentialBroker, ctx: TrustedToolContext): BrokerAuthorization {
  // Resolve and validate before creating any request closure. The runtime's
  // live authority is checked again by every request immediately before fetch.
  broker.identity(ctx);
  return {
    request: (method, path, data, idem) => broker.request(ctx, method, path, data, idem),
  };
}
