import { handleConsole as serveConsole } from "@reignhub/worker-core/console";

// The hosted site has its own public entry point. Both deployments serve the
// same standalone app and content-hashed assets.
export async function handleConsole(
  request: Request,
  env: Parameters<typeof serveConsole>[1],
) {
  const url = new URL(request.url);
  if (!/^\/(chat|web)\/?$/.test(url.pathname)) return;
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("Method not allowed", {
      status: 405,
      headers: { Allow: "GET, HEAD" },
    });
  }
  if (url.pathname.startsWith("/chat")) {
    url.pathname = "/web";
    return Response.redirect(url.href, 308);
  }
  url.pathname = "/chat";
  return serveConsole(new Request(url, request), env);
}
