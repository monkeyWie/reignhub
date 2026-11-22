export { externalRelays } from "./external-relay";
export { handleConsole } from "./console";
import { stunServers } from "./stun-servers";
import { drizzle } from "drizzle-orm/d1";
import { and, eq, gt, inArray } from "drizzle-orm";
import { APIError, PROTOCOL, routes, type Device } from "@reignhub/protocol";
import { devices, grants, pairings, type DeviceRow } from "./schema";
import { enrollNodes } from "./external-relay";
import { issueGrant, hash, id, random, serviceIdentity, un64 } from "./crypto";
import { unrestricted, type CoreEnv, type Policy } from "./types";
export * from "./schema";
export * from "./types";
export { issueGrant, serviceIdentity, hash, id, random } from "./crypto";
export { ReignHubRoom, RelayRoom } from "./room";
export function json(value: unknown, status = 200): Response {
  return Response.json(value, {
    status,
    headers: { "Cache-Control": "no-store" },
  });
}
export function fail(error: unknown): Response {
  if (error instanceof APIError)
    return json(
      { code: error.code, message: error.message, details: error.details },
      error.status,
    );
  console.error(
    "ReignHub request failed",
    error instanceof Error ? error.message : "unknown",
  );
  return json(
    { code: "SERVICE_UNAVAILABLE", message: "服务暂时不可用，请稍后重试" },
    503,
  );
}
async function bodyText(request: Request, limit: number): Promise<string> {
  if (Number(request.headers.get("Content-Length") || 0) > limit)
    throw new APIError("REQUEST_TOO_LARGE", "请求过大", 413);
  const reader = request.body?.getReader();
  let raw = "";
  let size = 0;
  if (reader) {
    const dec = new TextDecoder();
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) {
        await reader.cancel();
        throw new APIError("REQUEST_TOO_LARGE", "请求过大", 413);
      }
      raw += dec.decode(value, { stream: true });
    }
    raw += dec.decode();
  }
  return raw;
}
export async function body(request: Request): Promise<Record<string, unknown>> {
  const raw = await bodyText(request, 65536);
  try {
    const value = JSON.parse(raw || "{}");
    if (!value || typeof value !== "object" || Array.isArray(value))
      throw new Error();
    return value;
  } catch {
    throw new APIError("INVALID_REQUEST", "无效请求");
  }
}
export function originAllowed(request: Request, env: CoreEnv): boolean {
  const origin = request.headers.get("Origin");
  const own = new URL(request.url).origin;
  return (
    !origin ||
    origin === own ||
    (env.ALLOWED_ORIGINS || "")
      .split(",")
      .map((x) => x.trim())
      .includes(origin)
  );
}
export function credential(request: Request): string {
  const bearer = request.headers.get("Authorization");
  if (bearer?.startsWith("Bearer ")) return bearer.slice(7);
  const protocols = (request.headers.get("Sec-WebSocket-Protocol") || "")
    .split(",")
    .map((x) => x.trim());
  if (protocols.includes("reign.p2p")) {
    const token = protocols.find((x) => x.startsWith("auth."));
    if (token)
      try {
        return new TextDecoder().decode(un64(token.slice(5)));
      } catch {}
  }
  return "";
}
export async function authenticate(
  request: Request,
  env: CoreEnv,
): Promise<DeviceRow> {
  const token = credential(request);
  if (!token || token.length > 1024)
    throw new APIError("DEVICE_REQUIRED", "设备身份已失效，请重新登记", 401);
  const row = await drizzle(env.DB)
    .select()
    .from(devices)
    .where(eq(devices.tokenHash, await hash(token)))
    .get();
  if (!row)
    throw new APIError("DEVICE_REQUIRED", "设备身份已失效，请重新登记", 401);
  return row;
}
export function publicDevice(d: DeviceRow): Device {
  return {
    id: d.id,
    connectionId: d.connectionId,
    name: d.name,
    public: d.public,
    host: d.host,
    seen: d.seen,
  };
}
export async function canAccess(
  env: CoreEnv,
  deviceId: string,
  hostId: string,
): Promise<boolean> {
  return (
    deviceId === hostId ||
    !!(await drizzle(env.DB)
      .select()
      .from(grants)
      .where(and(eq(grants.host, hostId), eq(grants.client, deviceId)))
      .get())
  );
}
export function room(env: CoreEnv, host: string) {
  return env.ROOMS.get(env.ROOMS.idFromName("device:" + host));
}
export async function roomCall(
  env: CoreEnv,
  host: string,
  path: string,
  data: unknown,
): Promise<Response> {
  return room(env, host).fetch(
    new Request("https://room.internal" + path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    }),
  );
}
async function rateLimit(
  request: Request,
  env: CoreEnv,
  key: string,
  limit: number,
): Promise<void> {
  const ip = request.headers.get("CF-Connecting-IP") || "local";
  const keyHash = await hash(key + ":" + ip);
  const result = await roomCall(env, "rate:" + keyHash, "/limit", { limit });
  if (!result.ok)
    throw new APIError("RATE_LIMITED", "操作过于频繁，请稍后重试", 429);
}
export async function handleCore(
  request: Request,
  env: CoreEnv,
  policy: Policy = unrestricted,
): Promise<Response | null> {
  const url = new URL(request.url);
  if (url.pathname === "/api/account" && policy.deployment === "self-hosted")
    return json({
      deployment: "self-hosted",
      account: null,
      providers: [],
      plans: [],
    });
  if (!url.pathname.startsWith("/p2p/")) return null;
  try {
    if (!originAllowed(request, env))
      throw new APIError("ORIGIN_DENIED", "不允许的页面来源", 403);
    if (request.method === "OPTIONS")
      return new Response(null, {
        status: 204,
        headers: {
          "Access-Control-Allow-Origin":
            request.headers.get("Origin") || url.origin,
          "Access-Control-Allow-Headers": "Authorization, Content-Type",
          "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
          Vary: "Origin",
        },
      });
    let result: Response;
    if (url.pathname === routes.config) {
      const identity = await serviceIdentity(env);
      result = json({
        protocol: PROTOCOL,
        deployment: policy.deployment,
        accountRequired: policy.accountRequired?.(env) ?? false,
        loginProviders: policy.loginProviders(env),
        serviceId: identity.serviceId,
        grantPublicKey: identity.public,
        ...stunServers(env),
      });
    } else if (
      url.pathname === routes.signal ||
      url.pathname === routes.relay
    ) {
      if (
        request.method !== "GET" ||
        request.headers.get("Upgrade")?.toLowerCase() !== "websocket"
      )
        throw new APIError("UPGRADE_REQUIRED", "需要 WebSocket 连接", 426);
      const device = await authenticate(request, env);
      const hostID = url.searchParams.get("id") || "";
      const role = url.searchParams.get("role");
      if (
        !/^[a-f0-9]{32}$/.test(hostID) ||
        (role !== "host" && role !== "client")
      )
        throw new APIError("INVALID_ROUTE", "无效的连接目标");
      if (role === "host" && request.headers.has("Origin"))
        throw new APIError("INVALID_HOST", "浏览器不能作为被控端", 403);
      const host = await drizzle(env.DB)
        .select()
        .from(devices)
        .where(and(eq(devices.id, hostID), eq(devices.host, true)))
        .get();
      if (
        !host ||
        (role === "host" && device.id !== host.id) ||
        !(await canAccess(env, device.id, hostID))
      )
        throw new APIError("PAIRING_REQUIRED", "请先取得被控设备授权", 403);
      // Strip caller-supplied metadata. Only authenticated routing identity crosses this boundary.
      const headers = new Headers();
      headers.set("Upgrade", "websocket");
      headers.set("x-reignhub-device", device.id);
      if (request.headers.has("Sec-WebSocket-Protocol"))
        headers.set(
          "Sec-WebSocket-Protocol",
          request.headers.get("Sec-WebSocket-Protocol")!,
        );
      result = await room(env, hostID).fetch(
        new Request(request.url, { headers }),
      );
    } else {
      if (request.method !== "POST")
        throw new APIError("METHOD_NOT_ALLOWED", "请使用 POST", 405);
      const input = await body(request);
      const db = drizzle(env.DB);
      const now = Math.floor(Date.now() / 1000);
      if (url.pathname === routes.register) {
        await rateLimit(request, env, "register", 20);
        if (
          typeof input.public !== "string" ||
          !/^[a-f0-9]{64}$/.test(input.public) ||
          typeof input.name !== "string" ||
          input.name.length > 100 ||
          typeof input.isHost !== "boolean"
        )
          throw new APIError("INVALID_IDENTITY", "无效设备身份");
        const token = random();
        const deviceID = id();
        const identity = await serviceIdentity(env);
        let registered = false;
        let code = "";
        for (let tries = 0; tries < 12; tries++) {
          const n = new Uint32Array(1);
          crypto.getRandomValues(n);
          if (n[0] >= 4294000000) continue;
          code = String(n[0] % 1_000_000).padStart(6, "0");
          try {
            await db.insert(devices).values({
              id: deviceID,
              connectionId: code,
              name: input.name,
              public: input.public,
              tokenHash: await hash(token),
              host: input.isHost,
              seen: now,
            });
            registered = true;
            break;
          } catch (e) {
            if (!(e instanceof Error) || !e.message.includes("UNIQUE")) throw e;
          }
        }
        if (!registered)
          throw new APIError(
            "DEVICE_ID_EXHAUSTED",
            "设备编号暂时无法分配",
            503,
          );
        result = json({
          id: deviceID,
          connectionId: code,
          token,
          serviceId: identity.serviceId,
        });
      } else {
        const device = await authenticate(request, env);
        switch (url.pathname) {
          case routes.enrollment: {
            const hostID = input.host;
            if (
              (request.headers.has("Origin") && device.id === hostID) ||
              typeof hostID !== "string" ||
              !/^[a-f0-9]{32}$/.test(hostID) ||
              !(await canAccess(env, device.id, hostID))
            )
              throw new APIError("PAIRING_REQUIRED", "设备未获授权", 403);
            const host = await db
              .select()
              .from(devices)
              .where(and(eq(devices.id, hostID), eq(devices.host, true)))
              .get();
            if (!host) throw new APIError("INVALID_HOST", "无效被控端", 404);
            const role = device.id === hostID ? "host" : "client";
            const decision = await policy.admit(
              env,
              device,
              host,
              "connect",
              role,
            );
            // Managed, metered entitlements remain subject to their live policy.
            const durable = policy.deployment === "self-hosted";
            const signed = await issueGrant(
              env,
              hostID,
              device.id,
              role,
              decision,
              durable ? "private" : "managed",
              durable ? undefined : policy.relayUsageUrl?.(env),
            );
            const identity = await serviceIdentity(env);
            const nodes = decision.limits.allowRelay
              ? await enrollNodes(
                  env,
                  identity.serviceId,
                  durable ? "" : identity.public,
                  hostID,
                  device.id,
                  role,
                  signed.ticket,
                )
              : [];
            result = json({
              grant: signed.ticket,
              expires: signed.expires,
              node: nodes[0],
              alternates: nodes.length > 1 ? nodes.slice(1) : undefined,
            });
            result.headers.set("Cache-Control", "no-store");
            break;
          }
          case routes.presence: {
            const name =
              typeof input.name === "string" &&
              input.name.length <= 100 &&
              input.name
                ? input.name
                : device.name;
            // Persistent last-seen is coarsened; authoritative online state lives with sockets.
            if (now - device.seen >= 300 || name !== device.name)
              await db
                .update(devices)
                .set({ name, seen: now })
                .where(eq(devices.id, device.id));
            result = json({
              ok: true,
              accountPeers:
                device.host && input.accountAccess === true
                  ? await policy.accountPeers?.(env, device.id)
                  : undefined,
            });
            break;
          }
          case routes.status: {
            if (
              !Array.isArray(input.hosts) ||
              input.hosts.length > 50 ||
              input.hosts.some(
                (h) => typeof h !== "string" || !/^[a-f0-9]{32}$/.test(h),
              )
            )
              throw new APIError(
                "INVALID_REQUEST",
                "每次最多查询 50 台已配对设备",
              );
            const hosts = [...new Set(input.hosts as string[])];
            if (!hosts.length) {
              result = json({ devices: [] });
              break;
            }
            const allowed = new Set(
              (
                await db
                  .select({ host: grants.host })
                  .from(grants)
                  .where(
                    and(
                      eq(grants.client, device.id),
                      inArray(grants.host, hosts),
                    ),
                  )
              ).map((g) => g.host),
            );
            allowed.add(device.id);
            const rows = await db
              .select()
              .from(devices)
              .where(and(inArray(devices.id, hosts), eq(devices.host, true)));
            const visible = new Map(
              rows.filter((h) => allowed.has(h.id)).map((h) => [h.id, h]),
            );
            result = json({
              devices: await Promise.all(
                hosts.map(async (hostID) => {
                  const host = visible.get(hostID);
                  // Unknown and revoked devices reveal neither existence nor presence.
                  if (!host) return { host: hostID, online: null };
                  try {
                    const response = await roomCall(env, hostID, "/online", {});
                    if (!response.ok) throw new Error("presence unavailable");
                    const state = (await response.json()) as {
                      online: boolean;
                    };
                    return {
                      host: hostID,
                      name: host.name,
                      online: state.online,
                      checkedAt: now,
                    };
                  } catch {
                    return { host: hostID, online: null };
                  }
                }),
              ),
            });
            break;
          }
          case routes.resolve: {
            await rateLimit(request, env, "resolve", 60);
            const code = String(input.connectionId || "").replace(/[ -]/g, "");
            const host = await db
              .select()
              .from(devices)
              .where(
                and(eq(devices.connectionId, code), eq(devices.host, true)),
              )
              .get();
            if (!host)
              throw new APIError("DEVICE_NOT_FOUND", "找不到此设备", 404);
            const online = (await roomCall(env, host.id, "/online", {}).then(
              (r) => r.json(),
            )) as { online: boolean };
            result = json({
              accountBinding:
                online.online && input.accountAccess === true
                  ? await policy.accountBinding?.(env, device.id, host.id)
                  : undefined,
              device: publicDevice(host),
              online: online.online,
            });
            break;
          }
          case routes.request: {
            await rateLimit(request, env, "pair", 20);
            if (
              typeof input.id !== "string" ||
              !/^[a-f0-9]{32}$/.test(input.id) ||
              typeof input.host !== "string" ||
              typeof input.envelope !== "string" ||
              input.envelope.length > 32000
            )
              throw new APIError("INVALID_PAIRING", "无效配对申请");
            const host = await db
              .select()
              .from(devices)
              .where(and(eq(devices.id, input.host), eq(devices.host, true)))
              .get();
            if (!host)
              throw new APIError("DEVICE_NOT_FOUND", "被控设备不存在", 404);
            const entry = {
              id: input.id,
              host: host.id,
              client: device.id,
              name: device.name,
              public: device.public,
              envelope: input.envelope,
              state: "pending",
              expires: now + 120,
            };
            const exists = await db
              .select()
              .from(pairings)
              .where(eq(pairings.id, entry.id))
              .get();
            if (exists)
              throw new APIError("DUPLICATE_PAIRING", "配对申请已存在", 409);
            await db.insert(pairings).values(entry);
            result = json(entry);
            break;
          }
          case routes.pending:
            result = json(
              await db
                .select()
                .from(pairings)
                .where(
                  and(
                    eq(pairings.host, device.id),
                    eq(pairings.state, "pending"),
                    gt(pairings.expires, now),
                  ),
                ),
            );
            break;
          case routes.result: {
            const entry = await db
              .select()
              .from(pairings)
              .where(
                and(
                  eq(pairings.id, String(input.id)),
                  eq(pairings.client, device.id),
                  gt(pairings.expires, now),
                ),
              )
              .get();
            if (!entry)
              throw new APIError("PAIRING_EXPIRED", "配对申请已过期", 404);
            result = json(entry);
            break;
          }
          case routes.respond: {
            const entry = await db
              .select()
              .from(pairings)
              .where(
                and(
                  eq(pairings.id, String(input.id)),
                  eq(pairings.host, device.id),
                  eq(pairings.state, "pending"),
                  gt(pairings.expires, now),
                ),
              )
              .get();
            if (!entry)
              throw new APIError("PAIRING_EXPIRED", "配对申请已失效", 409);
            if (input.allow === true) {
              if (
                typeof input.reply !== "string" ||
                !input.reply ||
                input.reply.length > 32000
              )
                throw new APIError("INVALID_PAIRING", "缺少加密配对响应");
              await env.DB.batch([
                env.DB.prepare(
                  "INSERT INTO device_grants(host,client,pairing) SELECT host,client,id FROM pairings WHERE id=? AND host=? AND state='pending' AND expires>? ON CONFLICT(host,client) DO UPDATE SET pairing=excluded.pairing",
                ).bind(entry.id, device.id, now),
                env.DB.prepare(
                  "UPDATE pairings SET state='approved',reply=? WHERE id=? AND state='pending'",
                ).bind(input.reply, entry.id),
              ]);
            } else
              await db
                .update(pairings)
                .set({ state: "denied" })
                .where(
                  and(eq(pairings.id, entry.id), eq(pairings.state, "pending")),
                );
            result = json(
              await db
                .select()
                .from(pairings)
                .where(eq(pairings.id, entry.id))
                .get(),
            );
            break;
          }
          case routes.revoke: {
            const revoked = await db
              .delete(grants)
              .where(
                and(
                  eq(grants.host, device.id),
                  eq(grants.client, String(input.client)),
                  typeof input.pairing === "string" && input.pairing
                    ? eq(grants.pairing, input.pairing)
                    : undefined,
                ),
              );
            if (
              revoked.meta.changes &&
              !(await canAccess(env, String(input.client), device.id))
            )
              await roomCall(env, device.id, "/revoke", {
                device: String(input.client),
              });
            result = json({ ok: true });
            break;
          }
          default:
            throw new APIError("NOT_FOUND", "接口不存在", 404);
        }
      }
    }
    const origin = request.headers.get("Origin");
    if (origin && result.status !== 101) {
      result.headers.set("Access-Control-Allow-Origin", origin);
      result.headers.set("Vary", "Origin");
    }
    return result;
  } catch (e) {
    const response = fail(e);
    if (originAllowed(request, env) && request.headers.has("Origin")) {
      response.headers.set(
        "Access-Control-Allow-Origin",
        request.headers.get("Origin")!,
      );
      response.headers.set("Vary", "Origin");
    }
    return response;
  }
}
