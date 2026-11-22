import {
  env,
  SELF,
  applyD1Migrations,
  runInDurableObject,
} from "cloudflare:test";
import { beforeAll, describe, it, expect } from "vitest";
import { externalRelays } from "../src/external-relay";
import { routes } from "@reignhub/protocol";
import { handleCore, room, type CoreEnv } from "../src";
const bindings = env as unknown as CoreEnv & {
  TEST_RELAY_SIGNER: JsonWebKey;
  TEST_MIGRATIONS: Parameters<typeof applyD1Migrations>[1];
};
beforeAll(() => applyD1Migrations(bindings.DB, bindings.TEST_MIGRATIONS));
async function post(path: string, body: unknown, token?: string) {
  return SELF.fetch("https://reignhub.test" + path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: "Bearer " + token } : {}),
    },
    body: JSON.stringify(body),
  });
}
async function register(host = false) {
  const r = await post(routes.register, {
    name: host ? "Host" : "Browser",
    isHost: host,
    public: "ab".repeat(32),
  });
  expect(r.status).toBe(200);
  return (await r.json()) as {
    id: string;
    connectionId: string;
    token: string;
  };
}
async function socket(
  id: string,
  token: string,
  role: string,
  path: string = routes.signal,
  peer = "",
) {
  return SELF.fetch(
    `https://reignhub.test${path}?id=${id}&role=${role}&peer=${peer}`,
    { headers: { Upgrade: "websocket", Authorization: "Bearer " + token } },
  );
}
async function message(ws: WebSocket) {
  return new Promise<Record<string, unknown>>((resolve) =>
    ws.addEventListener(
      "message",
      (e) => resolve(JSON.parse(e.data as string)),
      { once: true },
    ),
  );
}
describe("device authorization and separated ingress", () => {
  it("publishes one namespace and never discloses the signing private key", async () => {
    const r = await post(routes.config, {});
    const c = (await r.json()) as Record<string, unknown>;
    expect(c.protocol).toBe(1);
    expect(c.selfStun).toEqual(["stun:probe.example:26331"]);
    expect(c.tcpStun).toEqual(["probe.example:26331"]);
    expect(c.grantPublicKey).toBeTruthy();
    expect(JSON.stringify(c)).not.toContain("private");
    expect((await post("/v2/register", {})).status).toBe(404);
  });
  it("publishes the configured STUN transports and honors explicit empty lists", async () => {
    async function config(
      value: CoreEnv["EXTERNAL_STUN"],
      previous: Partial<CoreEnv> = {},
    ) {
      const response = await handleCore(
        new Request("https://reignhub.test" + routes.config),
        {
          ...bindings,
          ...previous,
          EXTERNAL_STUN: value,
        },
      );
      return { status: response!.status, body: await response!.json() };
    }
    const publicStun = [
      "stun:stun.cloudflare.com:3478",
      "stun:stun.l.google.com:19302",
      "stun:stun.chat.bilibili.com:3478",
    ];
    expect(await config(undefined)).toMatchObject({
      body: {
        publicStun,
        selfStun: [],
        tcpStun: [],
      },
    });
    expect(await config("[]")).toMatchObject({
      body: { publicStun, selfStun: [], tcpStun: [] },
    });
    const previous = { PUBLIC_STUN: "", STUN_SERVERS: ["old.example:3478"] };
    expect(await config(undefined, previous)).toMatchObject({
      body: {
        publicStun,
        selfStun: ["stun:old.example:3478"],
        tcpStun: ["old.example:3478"],
      },
    });
    expect(await config([], previous)).toMatchObject({
      body: { publicStun, selfStun: [], tcpStun: [] },
    });
    expect(await config('["[::1]:3478","[::1]:3478"]')).toMatchObject({
      body: { selfStun: ["stun:[::1]:3478"], tcpStun: ["[::1]:3478"] },
    });
    expect(await config(["udp:udp.example:3478"])).toMatchObject({
      body: { selfStun: ["stun:udp.example:3478"], tcpStun: [] },
    });
    expect(await config(["tcp:tcp.example:3479"])).toMatchObject({
      body: { selfStun: [], tcpStun: ["tcp.example:3479"] },
    });
    expect(
      await config(["udp:stun.cloudflare.com:3478", "probe.example:26331"]),
    ).toMatchObject({
      body: {
        publicStun,
        selfStun: ["stun:probe.example:26331"],
        tcpStun: ["probe.example:26331"],
      },
    });
    expect(
      await config(undefined, { PUBLIC_STUN: "stun:old.example:3478" }),
    ).toMatchObject({
      body: { publicStun: [...publicStun, "stun:old.example:3478"] },
    });
    for (const value of [
      "{",
      '{"typo":[]}',
      '{"udp":null}',
      '["host:0"]',
      '["host:65536"]',
    ]) {
      expect(await config(value)).toMatchObject({
        status: 503,
        body: { code: "INVALID_STUN_CONFIG" },
      });
    }
  });
  it("resolves shared and single-transport relays and rejects conflicting addresses", () => {
    const pin = bindings.TEST_RELAY_SIGNER.x!;
    const resolve = (addresses: string[]) =>
      externalRelays({
        ...bindings,
        EXTERNAL_RELAYS: [
          {
            addresses,
            credential: `rh1.${pin}.${bindings.TEST_RELAY_SIGNER.d}`,
          },
        ],
      }).map((n) => n.endpoint);
    expect(resolve(["relay.example:26332"])).toEqual([
      { address: "relay.example:26332", pin },
    ]);
    expect(resolve(["tcp:[2001:db8::1]:443"])).toEqual([
      { tcpAddress: "[2001:db8::1]:443", pin },
    ]);
    expect(resolve(["udp:relay.example:26332"])).toEqual([
      { udpAddress: "relay.example:26332", pin },
    ]);
    expect(
      resolve(["relay.example:26332", "wss://Relay.Example:443/"]),
    ).toEqual([
      {
        address: "relay.example:26332",
        pin,
        wss: "wss://relay.example/p2p/node",
      },
    ]);
    for (const addresses of [
      ["wss://a/a"],
      ["wss://a", "wss://b"],
      ["wss://user@relay.example"],
      ["wss://relay.example/?x=1"],
      ["wss://relay.example:65536"],
      [],
      ["relay.example:26332", "tcp:relay.example:443"],
      ["udp://relay.example:443"],
      ["udp:[invalid]:443"],
    ]) {
      expect(() => resolve(addresses)).toThrow();
    }
  });
  it("rejects unauthenticated and hostile origins", async () => {
    expect(
      (await post(routes.resolve, { connectionId: "000000" })).status,
    ).toBe(401);
    expect(
      (
        await SELF.fetch("https://reignhub.test/p2p/config", {
          headers: { Origin: "https://evil.test" },
        })
      ).status,
    ).toBe(403);
  });
  it("bounds presence batches and requires an authenticated device", async () => {
    expect((await post(routes.status, { hosts: [] })).status).toBe(401);
    const c = await register();
    expect(
      (
        await post(
          routes.status,
          { hosts: Array(51).fill("ab".repeat(16)) },
          c.token,
        )
      ).status,
    ).toBe(400);
    expect(
      (await post(routes.status, { hosts: ["invalid"] }, c.token)).status,
    ).toBe(400);
    expect(
      await (await post(routes.status, { hosts: [] }, c.token)).json(),
    ).toEqual({ devices: [] });
  });
  it("pairs only after host consent and revocation closes all controller links", async () => {
    const h = await register(true),
      c = await register();
    expect(h.connectionId).toMatch(/^\d{6}$/);
    expect((await socket(h.id, c.token, "client")).status).toBe(403);
    const unknown = await post(
      routes.status,
      { hosts: [h.id, "ff".repeat(16)] },
      c.token,
    );
    expect(await unknown.json()).toEqual({
      devices: [
        { host: h.id, online: null },
        { host: "ff".repeat(16), online: null },
      ],
    });
    const hr = await socket(h.id, h.token, "host");
    expect(hr.status).toBe(101);
    const host = hr.webSocket!;
    host.accept();
    const hostWelcome = await message(host);
    const hostRelayResponse = await socket(
      h.id,
      h.token,
      "host",
      routes.relay,
      hostWelcome.peer as string,
    );
    const hostRelay = hostRelayResponse.webSocket!;
    hostRelay.accept();
    expect((await message(hostRelay)).nativeRelay).toBeUndefined();
    const p = crypto.randomUUID().replaceAll("-", "");
    await post(
      routes.request,
      { id: p, host: h.id, envelope: "encrypted request" },
      c.token,
    );
    const denied = await post(
      routes.respond,
      { id: p, allow: true, reply: "forged" },
      c.token,
    );
    expect(denied.status).toBe(409);
    expect(
      (
        await post(
          routes.respond,
          { id: p, allow: true, reply: "encrypted reply" },
          h.token,
        )
      ).status,
    ).toBe(200);
    const statuses = await post(routes.status, { hosts: [h.id] }, c.token);
    expect(
      (
        (await statuses.json()) as {
          devices: { online: boolean; name: string }[];
        }
      ).devices[0],
    ).toMatchObject({ online: true, name: "Host" });
    const cr = await socket(h.id, c.token, "client");
    expect(cr.status).toBe(101);
    const client = cr.webSocket!;
    client.accept();
    const welcome = await message(client);
    expect(welcome.allowRelay).toBe(true);
    expect(welcome.grant).toBeTruthy();
    const hostNode = (
      (await (
        await post(routes.enrollment, { host: h.id }, h.token)
      ).json()) as { node: { key: string; pin: string } }
    ).node;
    const clientNode = (
      (await (
        await post(routes.enrollment, { host: h.id }, c.token)
      ).json()) as { node: { key: string; pin: string } }
    ).node;
    expect(hostNode.pin).toEqual(clientNode.pin);
    expect(hostNode.key).not.toEqual(clientNode.key);
    expect(JSON.stringify(clientNode)).not.toContain(
      bindings.TEST_RELAY_SIGNER.d,
    );
    const dr = await socket(
      h.id,
      c.token,
      "client",
      routes.relay,
      welcome.peer as string,
    );
    expect(dr.status).toBe(101);
    const data = dr.webSocket!;
    data.accept();
    await message(data);
    const received = message(host);
    data.send(
      JSON.stringify({ kind: "chunk", body: "opaque", device: "forged" }),
    );
    const forwarded = await received;
    expect(forwarded.device).toBe(c.id);
    expect(forwarded.body).toBe("opaque");
    expect(forwarded.grant).toBeTruthy();
    const closed = new Promise((resolve) =>
      client.addEventListener("close", resolve),
    );
    expect((await post(routes.revoke, { client: c.id }, h.token)).status).toBe(
      200,
    );
    await closed;
    expect((await socket(h.id, c.token, "client")).status).toBe(403);
    host.close();
    data.close();
    hostRelay.close();
  });
  it("requires host consent for browser enrollment and denies browser host roles", async () => {
    const host = await register(true),
      other = await register();
    expect(
      (await post(routes.enrollment, { host: host.id }, other.token)).status,
    ).toBe(403);
    const res = await SELF.fetch("https://reignhub.test" + routes.enrollment, {
      method: "POST",
      headers: {
        Authorization: "Bearer " + host.token,
        Origin: "https://reignhub.test",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ host: host.id }),
    });
    expect(res.status).toBe(403);
    const request = crypto.randomUUID().replaceAll("-", "");
    await post(
      routes.request,
      { id: request, host: host.id, envelope: "fixture" },
      other.token,
    );
    await post(
      routes.respond,
      { id: request, allow: true, reply: "fixture" },
      host.token,
    );
    const allowed = await SELF.fetch(
      "https://reignhub.test" + routes.enrollment,
      {
        method: "POST",
        headers: {
          Authorization: "Bearer " + other.token,
          Origin: "https://reignhub.test",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ host: host.id }),
      },
    );
    expect(allowed.status).toBe(200);
    expect(await allowed.json()).toHaveProperty("grant");
  });
  it("isolates expired controllers from the host's shared reply socket", async () => {
    const h = await register(true),
      c = await register();
    const host = (await socket(h.id, h.token, "host")).webSocket!;
    host.accept();
    const hw = await message(host);
    const data = (
      await socket(h.id, h.token, "host", routes.relay, hw.peer as string)
    ).webSocket!;
    data.accept();
    await message(data);
    const p = crypto.randomUUID().replaceAll("-", "");
    await post(
      routes.request,
      { id: p, host: h.id, envelope: "encrypted" },
      c.token,
    );
    await post(
      routes.respond,
      { id: p, allow: true, reply: "encrypted" },
      h.token,
    );
    const client = (await socket(h.id, c.token, "client")).webSocket!;
    client.accept();
    const cw = await message(client);
    await runInDurableObject(room(bindings, h.id), (_instance, state) => {
      for (const ws of state.getWebSockets("client")) {
        const attachment = ws.deserializeAttachment();
        attachment.expires = 0;
        ws.serializeAttachment(attachment);
      }
    });
    data.send(
      JSON.stringify({
        peer: cw.peer,
        kind: "chunk",
        body: "encrypted late response",
      }),
    );
    // A late data reply cannot retire a P2P candidate's signal socket.
    client.send(JSON.stringify({ kind: "renew" }));
    expect((await message(client)).kind).toBe("lease");
    expect(host.readyState).toBe(WebSocket.OPEN);
    expect(data.readyState).toBe(WebSocket.OPEN);
    const replacement = (await socket(h.id, c.token, "client")).webSocket!;
    replacement.accept();
    await message(replacement);
    replacement.close();
    host.close();
    data.close();
  });
  it("keeps the host signal alive while replacing its response relay", async () => {
    const h = await register(true);
    const host = (await socket(h.id, h.token, "host")).webSocket!;
    host.accept();
    const welcome = await message(host);
    const data = (
      await socket(h.id, h.token, "host", routes.relay, welcome.peer as string)
    ).webSocket!;
    data.accept();
    await message(data);
    data.close(1000, "lost response channel");
    const replacement = await socket(
      h.id,
      h.token,
      "host",
      routes.relay,
      welcome.peer as string,
    );
    expect(replacement.status).toBe(101);
    replacement.webSocket!.accept();
    await message(replacement.webSocket!);
    expect(host.readyState).toBe(WebSocket.OPEN);
    const status = await post(routes.status, { hosts: [h.id] }, h.token);
    expect(
      ((await status.json()) as { devices: { online: boolean }[] }).devices[0]
        .online,
    ).toBe(true);
    replacement.webSocket!.close();
    host.close();
  });
  it("does not forward relay requests into a host with no reply socket", async () => {
    const h = await register(true),
      c = await register();
    const hr = await socket(h.id, h.token, "host");
    const host = hr.webSocket!;
    host.accept();
    await message(host);
    const p = crypto.randomUUID().replaceAll("-", "");
    await post(
      routes.request,
      { id: p, host: h.id, envelope: "encrypted" },
      c.token,
    );
    await post(
      routes.respond,
      { id: p, allow: true, reply: "encrypted" },
      h.token,
    );
    const cr = await socket(h.id, c.token, "client");
    const client = cr.webSocket!;
    client.accept();
    const welcome = await message(client);
    const dr = await socket(
      h.id,
      c.token,
      "client",
      routes.relay,
      welcome.peer as string,
    );
    const data = dr.webSocket!;
    data.accept();
    await message(data);
    const closed = new Promise<CloseEvent>((resolve) =>
      data.addEventListener("close", resolve, { once: true }),
    );
    data.send(JSON.stringify({ kind: "chunk", body: "opaque" }));
    // A late data reply cannot retire a P2P candidate's signal socket.
    client.send(JSON.stringify({ kind: "renew" }));
    expect((await message(client)).kind).toBe("lease");
    expect(host.readyState).toBe(WebSocket.OPEN);
    expect(client.readyState).toBe(WebSocket.OPEN);
    host.close();
    client.close();
    data.close();
  });
  it("does not allow business data on signaling", async () => {
    const h = await register(true);
    const hr = await socket(h.id, h.token, "host");
    const host = hr.webSocket!;
    host.accept();
    await message(host);
    const reply = message(host);
    host.send(JSON.stringify({ kind: "chunk", body: "opaque" }));
    expect((await reply).error).toBe("SIGNAL_ONLY");
    host.close();
  });
  it("allows the authenticated host to replace its lingering connection", async () => {
    const h = await register(true);
    const responses = await Promise.all([
      socket(h.id, h.token, "host"),
      socket(h.id, h.token, "host"),
    ]);
    expect(responses.map((r) => r.status)).toEqual([101, 101]);
    // Invalid credentials cannot replace the new host.
    expect((await socket(h.id, "wrong-token", "host")).status).toBe(401);
    const status = await post(
      routes.resolve,
      { connectionId: h.connectionId },
      h.token,
    );
    expect(((await status.json()) as { online: boolean }).online).toBe(true);
    for (const r of responses)
      if (r.webSocket) {
        r.webSocket.accept();
        r.webSocket.close();
      }
  });
});
