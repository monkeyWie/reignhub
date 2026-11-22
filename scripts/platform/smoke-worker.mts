// Run against an isolated Worker or Go server (--go-server).
// The spawned host only exposes a fake Agent, never real local sessions.
import assert from "node:assert/strict";
import { smokeSite } from "./smoke-site.mts";
import { execFile, spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { Service } from "../../packages/client-sdk/src/service";
import { Connection } from "../../packages/client-sdk/src/connection";
import type { ReignHubCache } from "../../packages/client-sdk/src/cache";

const origin = process.argv[2] || "http://127.0.0.1:8787";
const table = () => {
  const rows = new Map<string, unknown>();
  return {
    get: async (key: unknown) => rows.get(JSON.stringify(key)),
    put: async (row: Record<string, unknown>) => {
      rows.set(
        JSON.stringify(row.id ? [row.service, row.id] : row.service),
        row,
      );
      // Identity tables use the service key.
      if (row.token) rows.set(JSON.stringify(row.service), row);
    },
  };
};
const cache = {
  identities: table(),
  devices: table(),
} as unknown as ReignHubCache;
// Compile separately so cold dependency downloads do not consume the service
// readiness budget. The actual fixture still has to become ready in 60 seconds.
const fixtureDir = await mkdtemp(join(tmpdir(), "reignhub-service-smoke-"));
const fixture = join(fixtureDir, "host");
try {
  await promisify(execFile)(
    "go",
    ["build", "-o", fixture, "./testsupport/workerfixture"],
    {
      cwd: fileURLToPath(new URL("../../clients/nsgo", import.meta.url)),
      timeout: 180000,
    },
  );
} catch (error) {
  await rm(fixtureDir, { recursive: true, force: true });
  throw error;
}
const child = spawn(fixture, [origin], {
  detached: true,
  stdio: ["ignore", "pipe", "pipe"],
});
let stderr = "";
child.stderr.on("data", (b) => (stderr += b));
let client: Connection | undefined;
try {
  const code = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(Error("Fixture timed out: " + stderr)),
      60000,
    );
    let out = "";
    child.stdout.on("data", (b) => {
      out += b;
      const m = out.match(/"connectionId":"(\d{6})"/);
      if (m) {
        clearTimeout(timer);
        resolve(m[1]);
      }
    });
    child.on("exit", (n) => {
      clearTimeout(timer);
      reject(Error(`Fixture exited ${n}: ${stderr}`));
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
  if (process.argv[3] === "--site") {
    await smokeSite(origin);
  } else {
    const root = await fetch(origin + "/", { redirect: "manual" });
    assert.ok(
      [302, 307].includes(root.status),
      `root redirect: ${root.status}`,
    );
    assert.equal(
      new URL(root.headers.get("location")!, origin).pathname,
      "/chat/",
    );
    for (const path of [
      "/healthz",
      "/chat/",
      "/assets/chat/nsgo.json",
      "/api/account",
    ]) {
      assert.equal((await fetch(origin + path)).status, 200, path);
    }
    for (const path of [
      "/docs",
      "/zh-CN/docs",
      "/download",
      "/_next/static/missing.js",
      "/api/releases",
    ]) {
      assert.equal((await fetch(origin + path)).status, 404, path);
    }
    const shell = await (await fetch(origin + "/chat/")).text();
    assert.match(shell, /assets\/chat\/icon.svg/);
    assert.doesNotMatch(shell, /_next\//);
    assert.equal((await fetch(origin + "/assets/chat/icon.svg")).status, 200);
    const manifest = (await (
      await fetch(origin + "/assets/chat/nsgo.json")
    ).json()) as {
      wasmSize: number;
      wasmParts: { file: string; size: number }[];
    };
    assert.ok(manifest.wasmParts.length > 1);
    let bytes = 0;
    for (const part of manifest.wasmParts) {
      const response = await fetch(origin + "/assets/chat/" + part.file);
      assert.equal(response.status, 200, part.file);
      assert.equal(
        (await response.arrayBuffer()).byteLength,
        part.size,
        part.file,
      );
      bytes += part.size;
    }
    assert.equal(bytes, manifest.wasmSize);
  }
  const service = new Service(origin, cache);
  const config = await service.config();
  assert.equal(config.deployment, "self-hosted");
  assert.ok(config.grantPublicKey);
  const saved = await service.pair(code);
  client = await new Connection(
    saved,
    await service.identity(),
    config,
  ).connect();
  if (process.argv[3] === "--go-server")
    assert.equal(client.path, "Relay · WebSocket");
  const providers = await client.call<{ id: string }[]>({
    method: "providers",
  });
  assert.ok(providers.some((p) => p.id === "fixture"));
  const page = await client.call<{ sessions: { id: string }[] }>({
    method: "sessions",
    provider: "fixture",
  });
  assert.equal(page.sessions[0].id, "isolated");
  await client.call({
    method: "send",
    provider: "fixture",
    session: "isolated",
    text: "Worker ↔ Go signed-lease smoke test",
  });
  const transcript = await client.call({
    method: "transcript",
    provider: "fixture",
    session: "isolated",
  });
  assert.match(JSON.stringify(transcript), /signed-lease smoke test/);
  console.log(
    "Service HTTP, encrypted consent pairing, signed host access, sessions and send passed",
  );
} finally {
  client?.close();
  if (child.pid) {
    try {
      process.kill(-child.pid, "SIGTERM");
    } catch {}
  }
  child.stdout.destroy();
  child.stderr.destroy();
  await rm(fixtureDir, { recursive: true, force: true });
}
