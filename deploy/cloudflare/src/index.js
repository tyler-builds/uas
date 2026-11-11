// uas catalog Worker: serves the published catalog from R2 and accepts uploads from the indexer.
//
// Public:  GET/HEAD /manifest.json, /catalog-<sha256>.db.zst, /icons/<sha256>.png|svg,
//          /releases/latest.json, /releases/uas-<version>-linux-<arch> (the program) and its .tar.gz (the
//          archive uas 0.5.0 and older update from), and /releases/uas-linux-<arch>, a redirect to the latest
//          program, for download links
// Indexer: PUT /manifest.json, /catalog-<sha256>.db.zst or /icons/<sha256>.png|svg,
//          and (the release workflow) /releases/latest.json and release programs and archives,
//          DELETE /catalog-<sha256>.db.zst, GET /_catalogs and /_icons (list stored files).
//          These need "Authorization: Bearer <UPLOAD_TOKEN>".
//
// Icons are those of GitHub apps, read out of their AppImages by the indexer (Flathub hosts its own).
//
// Clients do not trust this Worker: they verify the manifest's ed25519 signature and the catalog's hash, and
// a release's signature and its archive's hash.
// The upload token only stops strangers from filling the bucket.

const MANIFEST = "manifest.json";
const CATALOG = /^catalog-[0-9a-f]{64}\.db\.zst$/;
const ICON = /^icons\/[0-9a-f]{64}\.(png|svg)$/;
const RELEASE_INFO = "releases/latest.json";
const RELEASE = /^releases\/uas-\d+\.\d+\.\d+-linux-(x86_64|aarch64)(\.tar\.gz)?$/;
const LATEST = /^releases\/uas-linux-(x86_64|aarch64)$/;
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
    const latest = LATEST.exec(key);
    if (latest) {
      if (request.method !== "GET" && request.method !== "HEAD") return text(405, "method not allowed");
      return redirectToLatest(request, env, latest[1]);
    }
    if (key !== MANIFEST && key !== RELEASE_INFO && !CATALOG.test(key) && !ICON.test(key) && !RELEASE.test(key)) {
      return text(404, "not found");
    }

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
  // Release archives are named by version, and a version is never published twice.
  const mutable = key === MANIFEST || key === RELEASE_INFO;
  headers.set("Cache-Control", mutable ? "public, max-age=300" : "public, max-age=31536000, immutable");
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
  let contentType = key === MANIFEST || key === RELEASE_INFO ? "application/json" : "application/zstd";
  if (ICON.test(key)) contentType = key.endsWith(".svg") ? "image/svg+xml" : "image/png";
  if (RELEASE.test(key)) {
    contentType = key.endsWith(".tar.gz") ? "application/gzip" : "application/octet-stream";
    // A published version is never replaced: clients and caches treat its archive as immutable.
    if ((await env.BUCKET.head(key)) !== null) return text(409, "this version is already published");
  }
  await env.BUCKET.put(key, request.body, { httpMetadata: { contentType } });
  return text(201, "stored");
}

// redirectToLatest sends a download link to the latest release's program for arch, as releases/latest.json
// names it. The redirect needs no trust: uas checks itself against the signed latest.json once installed, and
// the release's checksums are in it for anyone who checks by hand.
async function redirectToLatest(request, env, arch) {
  const obj = await env.BUCKET.get(RELEASE_INFO);
  if (obj === null) return text(404, "no release is published yet");
  let file;
  try {
    const release = JSON.parse(atob((await obj.json()).payload));
    file = (release.programs || []).find((f) => f.arch === arch);
  } catch {
    return text(500, "the published release can't be read");
  }
  if (!file || !RELEASE.test(file.path) || file.path.endsWith(".tar.gz")) {
    return text(404, `the latest release has no program for ${arch}`);
  }
  const location = new URL("/" + file.path, request.url).toString();
  return new Response(null, { status: 302, headers: { Location: location, "Cache-Control": "public, max-age=300" } });
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
