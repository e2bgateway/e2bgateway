// Small REST helper for examples that also run with the mock backend.
async function request(method, path, status, body) {
  let endpoint = process.env.E2B_API_URL || process.env.E2B_DOMAIN || "localhost:8080";
  if (!endpoint.includes("://")) {
    const host = new URL(`http://${endpoint}`).hostname;
    const scheme = ["localhost", "127.0.0.1", "[::1]"].includes(host) ? "http" : "https";
    endpoint = `${scheme}://${endpoint}`;
  }
  const url = new URL(endpoint);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error("set E2B_API_URL or E2B_DOMAIN to a gateway HTTP(S) URL");
  }
  const headers = { "X-API-Key": process.env.E2B_API_KEY || "test-key" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(url.href.replace(/\/+$/, "") + path, {
    method, headers, body: JSON.stringify(body),
    redirect: "error", // Never forward the gateway API key on redirect.
    signal: AbortSignal.timeout(15_000),
  });
  if (response.status !== status) {
    await response.body?.cancel();
    throw new Error(`${method} ${path}: expected HTTP ${status}, got ${response.status}`);
  }
  const payload = await response.text();
  return payload ? JSON.parse(payload) : undefined;
}

async function withSandbox(run) {
  const { sandboxID } = await request("POST", "/sandboxes", 201, { templateID: "base", timeout: 120 });
  if (!sandboxID) throw new Error("missing sandbox ID");
  const path = `/sandboxes/${encodeURIComponent(sandboxID)}`;
  try {
    await run(path);
  } finally {
    await request("DELETE", path, 204);
    console.log("Sandbox killed.");
  }
}

async function helloWorld() {
  await withSandbox(async (path) => {
    const marker = "Hello from E2BGateway!";
    const result = await request("POST", `${path}/commands`, 200, { command: `echo '${marker}'` });
    if (result.exitCode !== 0 || !result.stdout?.includes(marker)) {
      throw new Error(`unexpected command result: ${JSON.stringify(result)}`);
    }
    console.log(result.stdout.trim());
  });
}

async function codeExecution() {
  await withSandbox(async (path) => {
    const marker = "Hello from code execution!";
    const result = await request("POST", `${path}/code`, 200, { code: `print('${marker}')`, language: "python" });
    if (result.exitCode !== 0 || result.error || !result.stdout?.includes(marker)) {
      throw new Error(`unexpected code result: ${JSON.stringify(result)}`);
    }
    console.log(result.stdout.trim());
  });
}

async function filesystem() {
  await withSandbox(async (path) => {
    const filename = "/tmp/gateway example & test.txt";
    const text = "Hello from E2BGateway!";
    await request("POST", `${path}/files`, 204, { path: filename, content: text });
    const result = await request("GET", `${path}/files?${new URLSearchParams({ path: filename })}`, 200);
    if (result !== text) throw new Error(`file contents mismatch: ${JSON.stringify(result)}`);
    console.log(`Read back: ${result}`);
  });
}

module.exports = { request, helloWorld, codeExecution, filesystem };
