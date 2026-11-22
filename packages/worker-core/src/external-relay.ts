import { APIError } from "@reignhub/protocol";
import { configArray, transportEndpoints } from "./endpoint";
import { bytes64, un64 } from "./crypto";
import type { CoreEnv } from "./types";

export interface RelayNode {
  address?: string;
  udpAddress?: string;
  tcpAddress?: string;
  wss?: string;
  pin: string;
}

export function externalRelays(env: CoreEnv) {
  const error = () =>
    new APIError("INVALID_RELAY_CONFIG", "外部中继配置无效", 503);
  const entries = configArray(
    env.EXTERNAL_RELAYS === undefined ? [] : env.EXTERNAL_RELAYS,
    error,
  );
  const seen = new Set<string>();
  return entries.map((entry: unknown) => {
    if (
      !entry ||
      typeof entry !== "object" ||
      !("addresses" in entry) ||
      Object.keys(entry).some((k) => k !== "addresses" && k !== "credential") ||
      !("credential" in entry) ||
      typeof entry.credential !== "string"
    )
      throw error();
    const { addresses, credential } = entry;
    const match = /^rh1\.([A-Za-z0-9_-]{43})\.([A-Za-z0-9_-]{43})$/.exec(
      credential,
    );
    if (!match || bytes64(un64(match[2])) !== match[2]) throw error();
    const [, pin, secret] = match;
    if (!Array.isArray(addresses) || addresses.length > 32) throw error();
    let wss: string | undefined;
    const native = addresses.filter((address: unknown) => {
      if (typeof address !== "string" || !address.startsWith("wss://"))
        return true;
      let url: URL;
      try {
        url = new URL(address);
      } catch {
        throw error();
      }
      if (
        wss ||
        !/^wss:\/\/([a-zA-Z0-9.-]+|\[[a-fA-F0-9:.]+\])(:[0-9]+)?\/?$/.test(
          address,
        ) ||
        url.port === "0"
      )
        throw error();
      wss = url.origin + "/p2p/node";
      return false;
    });
    const { udp, tcp } = transportEndpoints(native, error);
    if (
      (!wss && udp.length + tcp.length === 0) ||
      udp.length > 1 ||
      tcp.length > 1 ||
      !/^[a-zA-Z0-9_-]{43}$/.test(pin) ||
      bytes64(un64(pin)) !== pin ||
      seen.has(pin)
    )
      throw error();
    seen.add(pin);
    const endpoint: RelayNode =
      udp[0] === tcp[0]
        ? { address: udp[0], pin }
        : { udpAddress: udp[0], tcpAddress: tcp[0], pin };
    if (wss) endpoint.wss = wss;
    return { endpoint, secret };
  });
}

export async function enrollNodes(
  env: CoreEnv,
  namespace: string,
  issuer: string,
  host: string,
  device: string,
  role: string,
  grant: string,
) {
  const nodes = externalRelays(env);
  if (!nodes.length) return [];
  const offset = parseInt(host.slice(0, 8), 16) % nodes.length;
  const count = issuer ? Math.min(3, nodes.length) : 1;
  return Promise.all(
    Array.from({ length: count }, async (_, idx) => {
      const node = nodes[(offset + idx) % nodes.length];
      const key = await crypto.subtle.importKey(
        "raw",
        un64(node.secret),
        { name: "HMAC", hash: "SHA-256" },
        false,
        ["sign"],
      );
      const proof = await crypto.subtle.sign(
        "HMAC",
        key,
        new TextEncoder().encode(
          ["reign-member-v2", namespace, issuer, host, device, role].join("."),
        ),
      );
      return {
        ...node.endpoint,
        namespace,
        issuer: issuer || undefined,
        grant: issuer ? grant : undefined,
        host,
        device,
        role,
        key: Array.from(new Uint8Array(proof), (b) =>
          b.toString(16).padStart(2, "0"),
        ).join(""),
      };
    }),
  );
}
