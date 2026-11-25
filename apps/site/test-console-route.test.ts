import assert from "node:assert/strict";
import { test } from "node:test";
import { handleConsole } from "./lib/console-route";

test("console uses the static NSGo shell at both entry paths, with no SSR or locale redirect", async () => {
  for (const path of ["/web", "/web/?demo=1"]) {
    const result = await handleConsole(
      new Request("https://example.com" + path),
      {
        ASSETS: {
          async fetch(r) {
            assert.equal(r.url, "https://example.com/assets/chat/index.html");
            return new Response("NSGo workspace", {
              headers: {
                "Content-Type": "text/html",
                "Cache-Control": "max-age=3600",
              },
            });
          },
        },
      },
    );
    assert.equal(result?.status, 200);
    assert.equal(result?.headers.get("Cache-Control"), "no-cache");
    assert.equal(result?.headers.get("X-Robots-Tag"), "noindex, nofollow");
    assert.equal(await result?.text(), "NSGo workspace");
  }
  assert.equal(
    await handleConsole(new Request("https://example.com/docs"), {}),
    undefined,
  );
  assert.equal(
    (
      await handleConsole(
        new Request("https://example.com/web", { method: "POST" }),
        {},
      )
    )?.status,
    405,
  );
});

test("old hosted links retain their device query", async () => {
  const response = await handleConsole(
    new Request("https://example.com/chat/?device=123456"),
    {},
  );
  assert.equal(response?.status, 308);
  assert.equal(
    response.headers.get("location"),
    "https://example.com/web?device=123456",
  );
});
