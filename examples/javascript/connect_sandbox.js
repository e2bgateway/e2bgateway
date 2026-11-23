/**
 * Connect to existing sandbox - Reconnect and interact.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node connect_sandbox.js
 *
 * Backend: an SDK-compatible backend that supports reconnect and file operations.
 * Run: node examples/javascript/connect_sandbox.js
 * Expected: The original sandbox ID and the state read after reconnecting.
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const apiKey = process.env.E2B_API_KEY || "test-key";
  const domain = process.env.E2B_DOMAIN || "localhost:8080";

  const sandbox = await Sandbox.create({ apiKey, domain, template: "base" });
  try {
    const path = "/tmp/state.txt";
    await sandbox.files.write(path, "persistent state");

    // Leave the sandbox alive while obtaining a new SDK handle.
    const reconnected = await Sandbox.connect(sandbox.sandboxId, { apiKey, domain });
    const state = await reconnected.files.read(path);
    if (state !== "persistent state") throw new Error(`unexpected sandbox state: ${JSON.stringify(state)}`);
    console.log(`Reconnected to ${sandbox.sandboxId}: ${state}`);
  } finally {
    await sandbox.kill();
    console.log("Sandbox killed.");
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
