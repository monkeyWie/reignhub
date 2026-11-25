// Read-only smoke checks, reusable against local builds and deployed sites.
import assert from "node:assert/strict";
import { publicLocalePath } from "../../apps/site/lib/language-routing";
import { pathToFileURL } from "node:url";
import { gunzipSync } from "node:zlib";

export async function smokeSite(origin: string) {
  const pending = new Set(
    ["en", "zh-CN"].flatMap((lang) =>
      ["", "/technology", "/pricing", "/download", "/docs"].map(
        (path) => publicLocalePath(path || "/", lang as "en" | "zh-CN"),
      ),
    ),
  );
  const visited = new Set<string>();
  let canonicalOrigin: string | undefined;
  while (pending.size) {
    const path = pending.values().next().value!;
    pending.delete(path);
    const response = await fetch(origin + path, {
      headers: { Cookie: "reignhub_locale=en" },
      redirect: "manual",
    });
    assert.equal(response.status, 200, path);
    const html = await response.text();
    const lang = path.startsWith("/zh-CN") ? "zh-CN" : "en";
    assert.match(html, new RegExp(`<html[^>]*lang="${lang}"`), path);
    assert.match(html, /<h1[\s>]/, path);
    assert.doesNotMatch(
      html,
      /https?:\/\/[^"<>\s]+\/en(?:\/|["<])/,
      `English-prefixed SEO URL: ${path}`,
    );
    assert.doesNotMatch(
      html,
      /\[object Object\]/,
      `Invalid rendered content: ${path}`,
    );
    assert.match(html, /<title>[^<]+ReignHub|<title>ReignHub/, path);
    assert.match(html, /<meta name="description" content="[^\"]+"/, path);
    assert.match(
      html,
      new RegExp(
        `property="og:locale" content="${lang === "en" ? "en_US" : "zh_CN"}"`,
      ),
      path,
    );
    const canonical = html.match(/<link rel="canonical" href="([^"]+)"/);
    if (canonical) {
      canonicalOrigin = new URL(canonical[1]).origin;
      assert.equal(canonical[1], canonicalOrigin + path, path);
      for (const alternate of ["en", "zh-CN", "x-default"]) {
        const alternatePath = publicLocalePath(
          path,
          alternate === "zh-CN" ? "zh-CN" : "en",
        );
        assert.ok(
          html.includes(
            `hrefLang="${alternate}" href="${canonicalOrigin}${alternatePath}"`,
          ) ||
            html.includes(
              `hreflang="${alternate}" href="${canonicalOrigin}${alternatePath}"`,
            ),
          `Missing alternate ${alternate}: ${path}`,
        );
      }
      assert.match(
        html,
        /property="og:image" content="https?:\/\/[^\"]+\/og\/(en|zh-CN)\.png"/,
        path,
      );
    }
    if (path === "/") {
      assert.match(html, /Agents on your Mac or PC/);
      assert.ok(
        !html.includes("Agent 在电脑上"),
        "English homepage must have translated content",
      );
    }
    if (path === "/" || path === "/zh-CN") {
      for (const text of [
        "/docs/self-hosting/go",
        "/docs/self-hosting/cloudflare",
        "Claude Code",
        "/docs/guides/terminal",
        "macOS",
        "Windows",
        "Linux",
        "iOS",
        "Android",
        "Web",
      ])
        assert.ok(
          html.includes(text),
          `Missing product highlight ${text}: ${path}`,
        );
    }
    const diagramKind =
      path.endsWith("/technology") || path.endsWith("/docs/architecture")
        ? "overview"
        : path.endsWith("/docs/architecture/stack")
          ? "stack"
          : path.endsWith("/docs/architecture/protocol") ||
              path.endsWith("/docs/guides/connections")
            ? "connection"
            : path.endsWith("/docs/agents/codex-app")
              ? "codex"
              : undefined;
    if (diagramKind)
      assert.ok(
        html.includes(`id="architecture-${diagramKind}-${lang}-title"`),
        `Missing localized architecture diagram: ${path}`,
      );
    assert.ok(
      !html.includes("/v2/"),
      `Internal migration history in public content: ${path}`,
    );
    visited.add(path);
    for (const match of html.matchAll(
      /href="(\/(?:zh-CN\/)?docs(?:\/[^"?#]*)?)(?:[?#][^"]*)?"/g,
    )) {
      const linked = match[1];
      if (!visited.has(linked)) pending.add(linked);
    }
    assert.ok(
      !/href="\/en(?:["/?#])/.test(html),
      `English prefix in public links: ${path}`,
    );
    if (lang === "zh-CN")
      for (const match of html.matchAll(
        /href="(\/(?:docs|features|technology|pricing|download)(?:[^"\s]*))"/g,
      )) {
        assert.equal(
          new URL(match[1], origin).searchParams.get("lang"),
          "en",
          `Chinese page has an unlocalized content link: ${path} -> ${match[1]}`,
        );
      }
  }
  for (const lang of ["en", "zh-CN"]) {
    for (const path of [
      "self-hosting/cloudflare",
      "contributing/development",
      "architecture/stack",
      "architecture/performance",
    ]) {
      assert.ok(
        visited.has(publicLocalePath(`/docs/${path}`, lang as "en" | "zh-CN")),
        `Unlinked translation: ${lang}/${path}`,
      );
    }
    assert.equal(
      (
        await fetch(
          origin +
            publicLocalePath(
              "/docs/not-a-reignhub-page",
              lang as "en" | "zh-CN",
            ),
          { headers: { Cookie: "reignhub_locale=en" } },
        )
      ).status,
      404,
    );
  }
  assert.equal((await fetch(origin + "/fr/docs")).status, 404);
  for (const path of ["/", "/docs/quick-start", "/download"]) {
    const neutral = await fetch(origin + path, { redirect: "manual" });
    assert.equal(neutral.status, 200, path);
    const chinese = await fetch(origin + path, {
      headers: { "Accept-Language": "zh-CN,en;q=0.8" },
      redirect: "manual",
    });
    assert.equal(chinese.status, 307, path);
    assert.equal(
      new URL(chinese.headers.get("Location")!, origin).pathname,
      publicLocalePath(path, "zh-CN"),
    );
    assert.match(chinese.headers.get("Cache-Control")!, /no-store/);
    const selected = await fetch(origin + path + "?lang=en&from=check", {
      headers: { "Accept-Language": "zh-CN", Cookie: "reignhub_locale=zh-CN" },
      redirect: "manual",
    });
    assert.equal(selected.status, 303);
    assert.match(selected.headers.get("Set-Cookie")!, /reignhub_locale=en/);
    assert.equal(
      new URL(selected.headers.get("Location")!, origin).search,
      "?from=check",
    );
    const remembered = await fetch(origin + path, {
      headers: { "Accept-Language": "zh-CN", Cookie: "reignhub_locale=en" },
      redirect: "manual",
    });
    assert.equal(remembered.status, 200, path);
  }
  assert.equal(
    (await fetch(origin + "/en/docs", { redirect: "manual" })).status,
    404,
  );
  const search = await fetch(origin + "/api/search");
  assert.equal(search.status, 200);
  const index = await search.json();
  assert.equal(index.type, "advanced");
  assert.equal(index.i18n, true, "Browser search must filter by locale");
  const serialized = JSON.stringify(index);
  assert.match(serialized, /自建/);
  assert.match(serialized, /Self-hosting/);
  for (const path of visited) {
    if (path.includes("/docs"))
      assert.ok(serialized.includes(path), `Search index missing ${path}`);
  }
  for (const path of ["/web", "/chat", "/account", "/admin"]) {
    const html = await (await fetch(origin + path)).text();
    assert.match(html, /name="robots" content="noindex, nofollow"/, path);
  }
  const consoleResponse = await fetch(origin + "/web");
  const consoleHTML = await consoleResponse.text();
  assert.doesNotMatch(consoleHTML, /_next\/|__next_f/);
  assert.match(consoleHTML, /DecompressionStream/);
  assert.match(consoleResponse.headers.get("Cache-Control") || "", /no-cache/);
  const config = JSON.parse(
    consoleHTML.match(/const config = (\{[^;]+\});/)![1],
  );
  const moduleResponse = await fetch(origin + "/assets/chat/" + config.wasm);
  assert.equal(moduleResponse.status, 200);
  assert.match(moduleResponse.headers.get("Cache-Control") || "", /immutable/);
  const compressed = Buffer.from(await moduleResponse.arrayBuffer());
  assert.ok(config.wasmParts?.length > 1);
  const parts = await Promise.all(
    config.wasmParts.map(async (part: { file: string; size: number }) => {
      const response = await fetch(origin + "/assets/chat/" + part.file);
      assert.equal(response.status, 200);
      assert.match(response.headers.get("Cache-Control") || "", /immutable/);
      const bytes = Buffer.from(await response.arrayBuffer());
      assert.equal(bytes.length, part.size);
      return bytes;
    }),
  );
  assert.deepEqual(Buffer.concat(parts), compressed);
  const moduleBytes = gunzipSync(compressed);
  assert.equal(moduleBytes.subarray(0, 4).toString("hex"), "0061736d");
  const robots = await (await fetch(origin + "/robots.txt")).text();
  for (const path of ["/web", "/chat", "/account", "/admin", "/api/", "/p2p/"])
    assert.ok(robots.includes(`Disallow: ${path}`));
  const sitemap = await (await fetch(origin + "/sitemap.xml")).text();
  assert.doesNotMatch(sitemap, /<loc>[^<]*\/en(?:\/|<)/);
  if (canonicalOrigin) {
    assert.ok(robots.includes(`Sitemap: ${canonicalOrigin}/sitemap.xml`));
    for (const path of visited)
      assert.ok(
        sitemap.includes(`<loc>${canonicalOrigin}${path}</loc>`),
        `Sitemap missing ${path}`,
      );
    assert.ok(sitemap.includes('hreflang="x-default"'));
  } else
    assert.ok(
      !sitemap.includes("<loc>"),
      "An unconfigured self-hosted site must not claim another domain",
    );
  for (const lang of ["en", "zh-CN"]) {
    const card = await fetch(`${origin}/og/${lang}.png`);
    assert.equal(card.status, 200);
    assert.match(card.headers.get("content-type") ?? "", /image\/png/);
  }
  console.log(
    `Bilingual website, ${visited.size - 10} linked docs, localized metadata/search, first-visit language choice, remembered preference, noindex and sitemap passed`,
  );
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  await smokeSite(process.argv[2] || "http://127.0.0.1:8787");
