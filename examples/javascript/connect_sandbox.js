/**
 * Connect to existing sandbox - Reconnect and interact.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node connect_sandbox.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const apiKey = process.env.E2B_API_KEY || "test-key";
  const domain = process.env.E2B_DOMAIN || "localhost:8080";

  // 1. Create a sandbox
  console.log("1. Creating sandbox...");
  const sandbox = await Sandbox.create({ apiKey, domain });
  const sandboxId = sandbox.sandboxId;
  console.log(`   Created: ${sandboxId}`);

  // 2. Write some state
  console.log("2. Writing state...");
  await sandbox.commands.run("echo 'persistent state' > /tmp/state.txt");
  await sandbox.kill();
  console.log(`   Disconnected from: ${sandboxId}`);

  // 3. List running sandboxes
  console.log("3. Listing sandboxes...");
  const sandboxes = await Sandbox.list({ apiKey, domain });
  console.log(`   Running: ${sandboxes.length}`);

  // 4. Connect to existing sandbox
  console.log("4. Reconnecting to sandbox...");
  try {
    const reconnected = await Sandbox.connect(sandboxId, { apiKey, domain });
    const result = await reconnected.commands.run("cat /tmp/state.txt");
    console.log(`   State: ${result.stdout.trim()}`);
    await reconnected.kill();
  } catch (e) {
    console.log(`   Connect failed (expected for mock): ${e.message}`);
  }

  console.log("\nDone.");
}

main().catch(console.error);
