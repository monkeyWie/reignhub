import { DurableObject } from "cloudflare:workers";
import { and, eq } from "drizzle-orm";
import { drizzle } from "drizzle-orm/d1";
import {
  APIError,
  PROTOCOL,
  SOCKET_PROTOCOL,
  type Frame,
  type Role,
  type Decision,
} from "@reignhub/protocol";
import { devices, type DeviceRow } from "./schema";
import { id, issueGrant } from "./crypto";
import { unrestricted, type CoreEnv, type Policy } from "./types";
import { body, canAccess, fail, json } from "./index";
interface SocketState {
  role: Role;
  route: "signal" | "relay";
  device: string;
  host: string;
  peer: string;
  generation: string;
  closed: boolean;
  holder?: string;
  usageUntil?: number;
  grant: string;
  expires: number;
  allowRelay: boolean;
  window: number;
  count: number;
  bytes: number;
}
const enc = new TextEncoder();
export class ReignHubRoom extends DurableObject<CoreEnv> {
  protected policy: Policy = unrestricted;
  private state(ws: WebSocket): SocketState | null {
    return ws.deserializeAttachment() as SocketState | null;
  }
  private sockets(
    role?: Role,
    route: "signal" | "relay" = "signal",
  ): WebSocket[] {
    return this.ctx.getWebSockets(route).filter((ws) => {
      const s = this.state(ws);
      return (
        s &&
        !s.closed &&
        ws.readyState === WebSocket.OPEN &&
        (!role || s.role === role)
      );
    });
  }
  private host(generation?: string): WebSocket | undefined {
    return this.sockets("host").find(
      (ws) => !generation || this.state(ws)?.generation === generation,
    );
  }
  private send(ws: WebSocket, f: Frame): void {
    try {
      ws.send(JSON.stringify(f));
    } catch {
      this.disconnect(ws, 1011, "connection unavailable");
    }
  }
  private disconnect(ws: WebSocket, code: number, reason: string): void {
    const s = this.state(ws);
    if (s && !s.closed) {
      s.closed = true;
      ws.serializeAttachment(s);
      if (s.route === "signal") {
        for (const child of this.sockets(undefined, "relay")) {
          const a = this.state(child)!;
          if (
            a.generation === s.generation &&
            (s.role === "host" || a.peer === s.peer)
          )
            this.disconnect(child, code, reason);
        }
        if (s.role === "host") {
          for (const client of this.sockets("client"))
            if (this.state(client)?.generation === s.generation)
              this.disconnect(client, 1013, "host disconnected");
        } else {
          const host = this.host(s.generation);
          if (host) this.send(host, { peer: s.peer, kind: "bye" });
        }
      }
      if (
        s.route === "relay" &&
        s.role === "client" &&
        s.holder &&
        s.usageUntil &&
        this.policy.useRelay
      ) {
        this.ctx.waitUntil(
          this.policy
            .useRelay(this.env, s.host, s.device, s.holder, true)
            .catch(() => {}),
        );
      }
      // A relay is optional: its failure must not revoke the host's signal
      // lease or close working direct peers. The host may reopen this relay
      // using the same signal peer and generation.
    }
    try {
      ws.close(code, reason.slice(0, 100));
    } catch {}
  }
  private async identities(
    deviceID: string,
    hostID: string,
  ): Promise<{ device: DeviceRow; host: DeviceRow }> {
    const db = drizzle(this.env.DB);
    const device = await db
      .select()
      .from(devices)
      .where(eq(devices.id, deviceID))
      .get();
    const host = await db
      .select()
      .from(devices)
      .where(and(eq(devices.id, hostID), eq(devices.host, true)))
      .get();
    if (!device || !host || !(await canAccess(this.env, deviceID, hostID)))
      throw new APIError("PAIRING_REQUIRED", "设备授权已撤销", 403);
    return { device, host };
  }
  private async forwardAllowed(
    ws: WebSocket,
    probe: boolean,
    size: number,
  ): Promise<boolean> {
    const s = this.state(ws)!;
    const now = Math.floor(Date.now() / 1000);
    if (!s.holder || s.closed || s.expires <= now || !s.allowRelay)
      return false;
    if (!this.policy.useRelay || (s.usageUntil ?? 0) > now) return true;
    if (probe) {
      const minute = Math.floor(now / 60);
      if (s.window !== minute) {
        s.window = minute;
        s.bytes = 0;
      }
      s.bytes += size;
      ws.serializeAttachment(s);
      if (s.bytes <= 65536) return true;
    }
    try {
      await this.policy.useRelay(this.env, s.host, s.device, s.holder, false);
    } catch (e) {
      this.send(ws, {
        kind: "error",
        error: e instanceof APIError ? e.code : "SERVICE_UNAVAILABLE",
      });
      this.disconnect(ws, 1008, "relay admission denied");
      return false;
    }
    const current = this.state(ws);
    if (!current || current.closed || current.holder !== s.holder) return false;
    current.usageUntil = now + 45;
    ws.serializeAttachment(current);
    return true;
  }
  private async decision(
    s: Pick<SocketState, "device" | "host" | "role">,
    operation: "connect" | "renew" | "relay",
  ): Promise<Decision> {
    const { device, host } = await this.identities(s.device, s.host);
    return this.policy.admit(this.env, device, host, operation, s.role);
  }
  async fetch(request: Request): Promise<Response> {
    return this.ctx.blockConcurrencyWhile(() => this.handle(request));
  }
  private async handle(request: Request): Promise<Response> {
    try {
      const url = new URL(request.url);
      if (url.hostname === "room.internal") {
        const input = await body(request);
        if (url.pathname === "/online") return json({ online: !!this.host() });
        if (url.pathname === "/account-removed") {
          const host = this.host();
          if (host)
            this.send(host, {
              peer: String(input.binding),
              kind: "account-removed",
              device: String(input.client),
            });
          return json({ ok: true });
        }
        if (url.pathname === "/revoke") {
          for (const ws of this.sockets("client"))
            if (this.state(ws)?.device === input.device)
              this.disconnect(ws, 1008, "device revoked");
          return json({ ok: true });
        }
        if (url.pathname === "/limit") {
          const now = Math.floor(Date.now() / 60000);
          const previous = await this.ctx.storage.get<{
            window: number;
            n: number;
          }>("rate");
          const value = {
            window: now,
            n: previous?.window === now ? previous.n + 1 : 1,
          };
          await this.ctx.storage.put("rate", value);
          return json(
            { ok: value.n <= Number(input.limit) },
            value.n <= Number(input.limit) ? 200 : 429,
          );
        }
        return json({ code: "NOT_FOUND" }, 404);
      }
      const role = url.searchParams.get("role") as Role;
      const hostID = url.searchParams.get("id")!;
      const deviceID = request.headers.get("x-reignhub-device")!;
      const relay = url.pathname === "/p2p/relay";
      const currentHost = this.host();
      let state: SocketState;
      if (relay) {
        const peer = url.searchParams.get("peer");
        const signal = this.sockets(role).find((ws) => {
          const s = this.state(ws)!;
          return s.peer === peer && s.device === deviceID && s.host === hostID;
        });
        if (!signal)
          throw new APIError("SIGNAL_REQUIRED", "请先建立信令连接", 409);
        state = {
          ...this.state(signal)!,
          route: "relay",
          holder: "ws:" + id(),
          window: 0,
          count: 0,
          bytes: 0,
        };
        const decision = await this.decision(state, "relay");
        if (!decision.limits.allowRelay)
          throw new APIError(
            "RELAY_REQUIRES_SUBSCRIPTION",
            "当前权益不包含官方中继",
            403,
          );
        for (const old of this.sockets(role, "relay"))
          if (this.state(old)?.peer === state.peer)
            this.disconnect(old, 1000, "replaced");
      } else {
        if (
          role === "host" &&
          currentHost &&
          this.state(currentHost)?.device !== deviceID
        )
          throw new APIError("HOST_ALREADY_CONNECTED", "设备已连接", 409);
        if (role === "client" && !currentHost)
          throw new APIError("HOST_OFFLINE", "被控设备离线", 503);
        if (role === "client" && this.sockets("client").length >= 32)
          throw new APIError("CONNECTION_LIMIT", "设备连接过多", 429);
        const peer = id();
        const decision = await this.decision(
          { device: deviceID, host: hostID, role },
          "connect",
        );
        const grant = await issueGrant(
          this.env,
          hostID,
          deviceID,
          role,
          decision,
        );
        state = {
          role,
          route: "signal",
          device: deviceID,
          host: hostID,
          peer,
          generation:
            role === "host" ? peer : this.state(currentHost!)!.generation,
          closed: false,
          grant: grant.ticket,
          expires: grant.expires,
          allowRelay: decision.limits.allowRelay,
          window: 0,
          count: 0,
          bytes: 0,
        };
        // Replace the previous host only after admission and relay selection succeed.
        if (role === "host" && currentHost)
          this.disconnect(currentHost, 1012, "host connection replaced");
      }
      const [client, server] = Object.values(new WebSocketPair());
      server.serializeAttachment(state);
      this.ctx.acceptWebSocket(server, [state.route, role]);
      // Auto-response bypasses JS and duration billing during idle periods.
      this.ctx.setWebSocketAutoResponse(
        new WebSocketRequestResponsePair("reign:ping", "reign:pong"),
      );
      this.send(server, {
        kind: "welcome",
        peer: state.peer,
        protocol: PROTOCOL,
        allowRelay: state.allowRelay,
        grant: state.grant,
      });
      const protocols = (request.headers.get("Sec-WebSocket-Protocol") || "")
        .split(",")
        .map((x) => x.trim());
      return new Response(null, {
        status: 101,
        webSocket: client,
        headers: protocols.includes(SOCKET_PROTOCOL)
          ? { "Sec-WebSocket-Protocol": SOCKET_PROTOCOL }
          : {},
      });
    } catch (e) {
      return fail(e);
    }
  }
  async webSocketMessage(
    ws: WebSocket,
    message: string | ArrayBuffer,
  ): Promise<void> {
    const state = this.state(ws);
    if (!state || state.closed) return;
    try {
      if (
        typeof message !== "string" ||
        enc.encode(message).byteLength >
          (state.route === "signal" ? 65536 : 2 << 20)
      )
        throw new APIError("INVALID_FRAME", "消息过大");
      let f: Frame;
      try {
        f = JSON.parse(message) as Frame;
      } catch {
        throw new APIError("INVALID_FRAME", "无效消息");
      }
      if (!f || typeof f.kind !== "string")
        throw new APIError("INVALID_FRAME", "无效消息");
      const now = Math.floor(Date.now() / 1000);
      if (
        state.route === "relay" &&
        state.role === "client" &&
        f.kind === "standby"
      ) {
        const holder = state.holder;
        state.holder = "ws:" + id();
        state.usageUntil = 0;
        ws.serializeAttachment(state);
        if (holder && this.policy.useRelay)
          await this.policy
            .useRelay(this.env, state.host, state.device, holder, true)
            .catch(() => {});
        return;
      }
      if (state.route === "signal" && f.kind === "renew") {
        const decision = await this.decision(state, "renew");
        const next = await issueGrant(
          this.env,
          state.host,
          state.device,
          state.role,
          decision,
        );
        state.grant = next.ticket;
        state.expires = next.expires;
        state.allowRelay = decision.limits.allowRelay;
        ws.serializeAttachment(state);
        this.send(ws, {
          kind: "lease",
          peer: state.peer,
          grant: state.grant,
          allowRelay: state.allowRelay,
        });
        if (state.role === "client") {
          const host = this.host(state.generation);
          if (host)
            this.send(host, {
              kind: "lease",
              peer: state.peer,
              device: state.device,
              grant: state.grant,
            });
        }
        for (const data of this.sockets(state.role, "relay")) {
          const leg = this.state(data)!;
          if (leg.peer !== state.peer) continue;
          if (!state.allowRelay) {
            this.disconnect(data, 1008, "relay entitlement expired");
            continue;
          }
          leg.expires = state.expires;
          leg.grant = state.grant;
          leg.allowRelay = true;
          data.serializeAttachment(leg);
        }
        return;
      }
      const auth = state;
      if (auth.expires <= now)
        throw new APIError("LEASE_EXPIRED", "连接授权已到期");
      if (state.route === "signal") {
        if (f.kind !== "hello" && f.kind !== "signal")
          throw new APIError("SIGNAL_ONLY", "业务数据必须使用中继或直连");
        const window = Math.floor(now / 60);
        if (state.window !== window) {
          state.window = window;
          state.count = 0;
          state.bytes = 0;
        }
        state.count++;
        state.bytes += enc.encode(message).byteLength;
        if (state.count > 240 || state.bytes > 512 * 1024)
          throw new APIError("SIGNAL_RATE_LIMIT", "信令过于频繁");
        ws.serializeAttachment(state);
      } else {
        if (!auth.allowRelay)
          throw new APIError(
            "RELAY_REQUIRES_SUBSCRIPTION",
            "当前权益不包含中继",
          );
        if (f.kind !== "chunk" && f.kind !== "data")
          throw new APIError("INVALID_FRAME", "中继只接受加密业务帧");
      }
      if (typeof f.body !== "string")
        throw new APIError("INVALID_FRAME", "缺少加密内容");
      if (state.role === "host") {
        if (!this.host(state.generation))
          throw new APIError("HOST_OFFLINE", "设备离线");
        const target = this.sockets("client").find((c) => {
          const s = this.state(c)!;
          return s.peer === f.peer && s.generation === state.generation;
        });
        if (target) {
          const targetAuth = this.state(target)!;
          if (state.route === "relay") {
            const data = this.sockets("client", "relay").find(
              (s) => this.state(s)?.peer === targetAuth.peer,
            );
            if (
              !data ||
              !(await this.forwardAllowed(
                data,
                f.probe === true,
                f.body.length,
              ))
            )
              return;
          } else if (targetAuth.expires <= now) {
            this.disconnect(target, 1008, "signaling authorization expired");
            return;
          }
          this.send(target, {
            peer: targetAuth.peer,
            kind: f.kind,
            body: f.body,
          });
        }
      } else {
        const target = this.host(state.generation);
        if (!target) throw new APIError("HOST_OFFLINE", "设备离线");
        if (
          state.route === "relay" &&
          !this.sockets("host", "relay").some(
            (s) => this.state(s)?.generation === state.generation,
          )
        ) {
          // Reject this relay path promptly while the host repairs its reply
          // socket. Leave its signal lease and other direct peers intact.
          throw new APIError(
            "HOST_RELAY_UNAVAILABLE",
            "被控设备中继已断开，正在重新接入",
            503,
          );
        }
        if (
          state.route === "relay" &&
          !(await this.forwardAllowed(ws, f.probe === true, f.body.length))
        )
          return;
        this.send(target, {
          peer: state.peer,
          device: state.device,
          kind: f.kind,
          body: f.body,
          grant: auth.grant,
        });
      }
    } catch (e) {
      this.send(ws, {
        kind: "error",
        error: e instanceof APIError ? e.code : "SERVICE_UNAVAILABLE",
      });
      this.disconnect(
        ws,
        1008,
        e instanceof Error ? e.message : "request failed",
      );
    }
  }
  webSocketClose(ws: WebSocket, code: number, reason: string): void {
    this.disconnect(ws, code, reason || "closed");
  }
  webSocketError(ws: WebSocket): void {
    this.disconnect(ws, 1011, "socket error");
  }
}
export class RelayRoom extends ReignHubRoom {}
