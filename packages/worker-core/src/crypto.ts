import type { Grant, Role, Decision } from "@reignhub/protocol";
import type { CoreEnv } from "./types";
const enc = new TextEncoder();
export function bytes64(b: Uint8Array): string {
  let s = "";
  for (const n of b) s += String.fromCharCode(n);
  return btoa(s).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}
export function un64(s: string): Uint8Array<ArrayBuffer> {
  const raw = atob(s.replaceAll("-", "+").replaceAll("_", "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}
export function random(): string {
  return (
    crypto.randomUUID().replaceAll("-", "") +
    crypto.randomUUID().replaceAll("-", "")
  );
}
export function id(): string {
  return crypto.randomUUID().replaceAll("-", "");
}
export async function hash(s: string): Promise<string> {
  return [
    ...new Uint8Array(await crypto.subtle.digest("SHA-256", enc.encode(s))),
  ]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
interface Keys {
  serviceId: string;
  public: string;
  private: string;
}
export async function serviceIdentity(env: CoreEnv): Promise<Keys> {
  let row = await env.DB.prepare(
    "SELECT value FROM metadata WHERE key='identity'",
  ).first<{ value: string }>();
  if (!row) {
    const keys = (await crypto.subtle.generateKey("Ed25519", true, [
      "sign",
      "verify",
    ])) as CryptoKeyPair;
    const value: Keys = {
      serviceId: id(),
      public: bytes64(
        new Uint8Array(
          (await crypto.subtle.exportKey("raw", keys.publicKey)) as ArrayBuffer,
        ),
      ),
      private: bytes64(
        new Uint8Array(
          (await crypto.subtle.exportKey(
            "pkcs8",
            keys.privateKey,
          )) as ArrayBuffer,
        ),
      ),
    };
    await env.DB.prepare(
      "INSERT OR IGNORE INTO metadata(key,value) VALUES('identity',?)",
    )
      .bind(JSON.stringify(value))
      .run();
    row = await env.DB.prepare(
      "SELECT value FROM metadata WHERE key='identity'",
    ).first<{ value: string }>();
  }
  if (!row) throw new Error("Service identity unavailable");
  return JSON.parse(row.value) as Keys;
}
export async function issueGrant(
  env: CoreEnv,
  hostId: string,
  deviceId: string,
  role: Role,
  decision: Decision,
  mode: "online" | "private" | "managed" = "online",
  relayUsageUrl?: string,
): Promise<{ ticket: string; expires: number }> {
  const identity = await serviceIdentity(env);
  const now = Math.floor(Date.now() / 1000);
  const grant: Grant = {
    aud: "reign-connect-v1",
    namespace: identity.serviceId,
    hostId,
    deviceId,
    role,
    scopes: [
      "connect",
      ...(mode !== "online" ? ["offline"] : []),
      ...(decision.limits.allowRelay ? ["relay"] : []),
    ],
    products: decision.products,
    iat: now,
    exp: mode === "private" ? 0 : now + 180,
    jti: id(),
    relayUsageUrl,
  };
  const payload = bytes64(enc.encode(JSON.stringify(grant)));
  const key = await crypto.subtle.importKey(
    "pkcs8",
    un64(identity.private),
    "Ed25519",
    false,
    ["sign"],
  );
  const signature = await crypto.subtle.sign(
    "Ed25519",
    key,
    enc.encode("reign/grant/v1/" + payload),
  );
  return {
    ticket: payload + "." + bytes64(new Uint8Array(signature)),
    expires: grant.exp,
  };
}
