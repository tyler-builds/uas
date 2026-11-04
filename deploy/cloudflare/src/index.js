// uas catalog Worker: serves the published catalog from R2 and accepts uploads from the indexer.
//
// Public:  GET/HEAD /manifest.json, /catalog-<sha256>.db.zst, /icons/<sha256>.png|svg
// Indexer: PUT /manifest.json, /catalog-<sha256>.db.zst or /icons/<sha256>.png|svg,
//          DELETE /catalog-<sha256>.db.zst, GET /_catalogs and /_icons (list stored files).
//          These need "Authorization: Bearer <UPLOAD_TOKEN>".
//
// Icons are those of GitHub apps, read out of their AppImages by the indexer (Flathub hosts its own).
//
// Clients do not trust this Worker: they verify the manifest's ed25519 signature and the catalog's hash.
// The upload token only stops strangers from filling the bucket.

const MANIFEST = "manifest.json";
const CATALOG = /^catalog-[0-9a-f]{64}\.db\.zst$/;
const ICON = /^icons\/[0-9a-f]{64}\.(png|svg)$/;
const MAX_ICON = 1024 * 1024;
const MAX_UPLOAD = 100 * 1024 * 1024;

export default {
  async fetch(request, env) {
    const key = decodeURIComponent(new URL(request.url).pathname.slice(1));

    if (key === "_catalogs" || key === "_icons") {
      if (request.method !== "GET") return text(405, "method not allowed");
      if (!(await authorized(request, env))) return text(401, "unauthorized");
      return key === "_catalogs" ? listCatalogs(env) : listIcons(env);
    }
    if (key !== MANIFEST && !CATALOG.test(key) && !ICON.test(key)) return text(404, "not found");

    switch (request.method) {
      case "GET":
      case "HEAD":
        return serve(request, env, key);
      case "PUT":
        if (!(await authorized(request, env))) return text(401, "unauthorized");
        return put(request, env, key);
      case "DELETE":
        if (!(await authorized(request, env))) return text(401, "unauthorized");
        if (!CATALOG.test(key)) return text(403, "only catalogs can be deleted");
        await env.BUCKET.delete(key);
        return new Response(null, { status: 204 });
      default:
        return text(405, "method not allowed");
    }
  },
};

async function serve(request, env, key) {
  const obj = await env.BUCKET.get(key, { onlyIf: request.headers });
  if (obj === null) return text(404, "not found");

  const headers = new Headers();
  obj.writeHttpMetadata(headers);
  headers.set("ETag", obj.httpEtag);
  // Catalog files and icons are named by their hash, so they never change.
  headers.set("Cache-Control", key === MANIFEST ? "public, max-age=300" : "public, max-age=31536000, immutable");
  if (ICON.test(key)) {
    // Icons come from third-party AppImages. An SVG opened on its own must not run scripts.
    headers.set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox");
    headers.set("X-Content-Type-Options", "nosniff");
  }

  // With a matching If-None-Match, R2 returns the object without a body.
  if (!("body" in obj)) return new Response(null, { status: 304, headers });
  headers.set("Content-Length", String(obj.size));
  return new Response(request.method === "HEAD" ? null : obj.body, { headers });
}

async function put(request, env, key) {
  const length = Number(request.headers.get("Content-Length"));
  if (!Number.isInteger(length) || length <= 0) return text(411, "Content-Length required");
  if (length > (ICON.test(key) ? MAX_ICON : MAX_UPLOAD)) return text(413, "too large");
  let contentType = key === MANIFEST ? "application/json" : "application/zstd";
  if (ICON.test(key)) contentType = key.endsWith(".svg") ? "image/svg+xml" : "image/png";
  await env.BUCKET.put(key, request.body, { httpMetadata: { contentType } });
  return text(201, "stored");
}

async function listIcons(env) {
  const keys = [];
  let cursor;
  do {
    const page = await env.BUCKET.list({ prefix: "icons/", cursor });
    for (const o of page.objects) if (ICON.test(o.key)) keys.push(o.key);
    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);
  return new Response(JSON.stringify(keys), { headers: { "Content-Type": "application/json" } });
}

async function listCatalogs(env) {
  const objects = [];
  let cursor;
  do {
    const page = await env.BUCKET.list({ prefix: "catalog-", cursor });
    for (const o of page.objects) {
      if (CATALOG.test(o.key)) objects.push({ key: o.key, size: o.size, uploaded: o.uploaded.toISOString() });
    }
    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);
  return new Response(JSON.stringify(objects), { headers: { "Content-Type": "application/json" } });
}

async function authorized(request, env) {
  if (!env.UPLOAD_TOKEN) return false;
  const enc = new TextEncoder();
  const got = enc.encode(request.headers.get("Authorization") || "");
  const want = enc.encode(`Bearer ${env.UPLOAD_TOKEN}`);
  // Compare hashes so the comparison takes the same time whatever the input length.
  const [a, b] = await Promise.all([crypto.subtle.digest("SHA-256", got), crypto.subtle.digest("SHA-256", want)]);
  return crypto.subtle.timingSafeEqual(a, b);
}

function text(status, body) {
  return new Response(body + "\n", { status, headers: { "Content-Type": "text/plain; charset=utf-8" } });
}
