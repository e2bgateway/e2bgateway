/**
 * Warm Pools - manage pre-warmed sandbox pools over the gateway REST API.
 *
 * Uses native fetch (Node >= 18); no npm dependencies (warm pools are not
 * part of the E2B SDK surface).
 *
 * Demonstrates:
 * 1. List warm pools
 * 2. Create a warm pool
 * 3. Get warm pool details
 * 4. Update pool size
 * 5. Delete warm pool (graceful)
 * 6. Force delete (?force=true — immediate teardown)
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node warm_pools.js
 */

let baseUrl = process.env.E2B_DOMAIN || "localhost:8080";
if (!baseUrl.startsWith("http")) {
  // Plain HTTP only for loopback; a remote gateway must receive the API key
  // over TLS.
  const host = baseUrl.split("/")[0].split(":")[0].replace(/[\[\]]/g, "");
  const loopback = ["localhost", "127.0.0.1", "::1"].includes(host);
  baseUrl = `${loopback ? "http" : "https"}://${baseUrl}`;
}
const headers = {
  "X-API-Key": process.env.E2B_API_KEY || "test-key",
  "Content-Type": "application/json",
};

const created = []; // pools to best-effort clean up on failure

// Run-unique, RFC 1123-compatible pool IDs: force delete tombstones the
// namespace briefly, so the deterministic auto-derived ID (wp-base-…) would
// collide with the tombstone on an immediate re-run.
const uniquePoolID = (prefix) => `${prefix}-${Date.now()}`;

async function request(method, path, { body, query } = {}) {
  const url = new URL(path, baseUrl);
  for (const [k, v] of Object.entries(query || {})) url.searchParams.set(k, v);
  const resp = await fetch(url, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!resp.ok) {
    throw new Error(`${method} ${path} -> HTTP ${resp.status}: ${await resp.text()}`);
  }
  return resp.status === 204 ? null : resp.json();
}

async function main() {
  console.log("1. Listing warm pools...");
  const pools = await request("GET", "/warm-pools");
  console.log(`   Existing pools: ${pools.length}`);

  console.log("2. Creating warm pool...");
  const pool = await request("POST", "/warm-pools", {
    body: { templateID: "base", targetSize: 3, warmPoolID: uniquePoolID("wp-js") },
  });
  created.push(pool.warmPoolID);
  console.log(`   Pool: ${pool.warmPoolID} (template=${pool.templateID}, target=${pool.targetSize})`);

  console.log("3. Getting warm pool details...");
  const detail = await request("GET", `/warm-pools/${pool.warmPoolID}`);
  console.log(`   Status: ${detail.status}, target=${detail.targetSize}, current=${detail.currentSize}`);

  console.log("4. Updating pool size...");
  await request("POST", `/warm-pools/${pool.warmPoolID}/size`, { body: { targetSize: 5 } });
  console.log("   Updated");

  console.log("5. Deleting warm pool (graceful)...");
  await request("DELETE", `/warm-pools/${pool.warmPoolID}`);
  created.length = 0;
  console.log("   Deleted");

  console.log("6. Force-deleting a warm pool...");
  const forcePool = await request("POST", "/warm-pools", {
    body: { templateID: "base", targetSize: 1, warmPoolID: uniquePoolID("wp-js-force") },
  });
  created.push(forcePool.warmPoolID);
  await request("DELETE", `/warm-pools/${forcePool.warmPoolID}`, { query: { force: "true" } });
  created.length = 0;
  console.log(`   Force deleted ${forcePool.warmPoolID}`);

  console.log("\nDone.");
}

main().catch(async (err) => {
  console.error("ERROR:", err.message);
  for (const id of created) {
    try {
      await request("DELETE", `/warm-pools/${id}`, { query: { force: "true" } });
    } catch {
      /* best-effort */
    }
  }
  process.exit(1);
});
