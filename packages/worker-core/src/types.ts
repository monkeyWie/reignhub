import type { Decision, Role } from "@reignhub/protocol";
import type { DeviceRow } from "./schema";
export interface CoreEnv {
  DB: D1Database;
  ROOMS: DurableObjectNamespace;
  ALLOWED_ORIGINS?: string;
  PUBLIC_STUN?: string;
  EXTERNAL_STUN?: string | string[];
  SELF_STUN?: string;
  TCP_STUN?: string;
  STUN_SERVERS?: string | string[];
  PUBLIC_URL?: string;
  EXTERNAL_RELAYS?: string | { addresses: string[]; credential: string }[];
}
export interface Policy {
  deployment: "self-hosted" | "saas";
  loginProviders(env: CoreEnv): string[];
  accountRequired?(env: CoreEnv): boolean;
  accountBinding?(
    env: CoreEnv,
    client: string,
    host: string,
  ): Promise<string | undefined>;
  accountPeers?(
    env: CoreEnv,
    host: string,
  ): Promise<{ client: string; public: string; binding: string }[]>;
  relayUsageUrl?(env: CoreEnv): string;
  useRelay?(
    env: CoreEnv,
    host: string,
    client: string,
    holder: string,
    release: boolean,
  ): Promise<void>;
  admit(
    env: CoreEnv,
    device: DeviceRow,
    host: DeviceRow,
    operation: "connect" | "renew" | "relay",
    role: Role,
  ): Promise<Decision>;
  release?(env: CoreEnv, device: DeviceRow, host: DeviceRow): Promise<void>;
}
export const unrestricted: Policy = {
  deployment: "self-hosted",
  loginProviders: () => [],
  async admit(_env, _device, host) {
    return {
      limits: {
        maxHostDevices: null,
        maxAgentsPerDevice: null,
        maxConcurrentConnections: null,
        allowRelay: true,
      },
      products: null,
      quotaKey: host.id,
      revision: "self-hosted",
    };
  },
};
