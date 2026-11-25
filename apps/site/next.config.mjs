import { createMDX } from "fumadocs-mdx/next";
import { initOpenNextCloudflareForDev } from "@opennextjs/cloudflare";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The site also builds inside the commercial repository's pnpm workspace.
// Include its dependency store above the pinned public submodule.
let workspaceRoot = fileURLToPath(new URL("../..", import.meta.url));
for (let dir = workspaceRoot; ; dir = dirname(dir)) {
  if (existsSync(join(dir, "pnpm-workspace.yaml"))) workspaceRoot = dir;
  if (dirname(dir) === dir) break;
}
if (process.env.NODE_ENV === "development") void initOpenNextCloudflareForDev();
const config = {
  turbopack: { root: workspaceRoot },
  outputFileTracingRoot:
    process.env.NEXT_PRIVATE_OUTPUT_TRACE_ROOT || workspaceRoot,
  transpilePackages: ["@reignhub/account-ui"],
  images: { unoptimized: true },
  poweredByHeader: false,
  redirects() {
    return ["", "/zh-CN"].flatMap((prefix) => [
      {
        source: prefix + "/features",
        destination: (prefix || "/") + "#features",
        permanent: true,
      },
      ...["codex-app", "codex-cli"].map((slug) => ({
        source: `${prefix}/docs/agents/${slug}`,
        destination: `${prefix}/docs/agents/codex`,
        permanent: true,
      })),
    ]);
  },
};
export default createMDX()(config);
