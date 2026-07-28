/**
 * Sandbox Lifecycle - Create, pause, resume, set timeout, and kill.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node sandbox_lifecycle.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const apiKey = process.env.E2B_API_KEY || "test-key";
  const domain = process.env.E2B_DOMAIN || "localhost:8080";

  // 1. Create sandbox
  console.log("1. Creating sandbox...");
  const sandbox = await Sandbox.create({ apiKey, domain });
  console.log(`   Created: ${sandbox.sandboxId}`);

  // 2. List running sandboxes
  console.log("2. Listing sandboxes...");
  const sandboxes = await Sandbox.list({ apiKey, domain });
  console.log(`   Running sandboxes: ${sandboxes.length}`);
  for (const sbx of sandboxes) {
    console.log(`   - ${sbx.sandboxId} (template: ${sbx.templateId})`);
  }

  // 3. Pause sandbox
  console.log("3. Pausing sandbox...");
  const pausedId = await sandbox.pause();
  console.log(`   Paused: ${pausedId}`);

  // 4. Resume sandbox
  console.log("4. Resuming sandbox...");
  const resumed = await Sandbox.resume(pausedId, { apiKey, domain });
  console.log(`   Resumed: ${resumed.sandboxId}`);

  // 5. Set timeout
  console.log("5. Setting timeout to 300 seconds...");
  await resumed.setTimeout(300);
  console.log("   Timeout set.");

  // 6. Kill sandbox
  console.log("6. Killing sandbox...");
  await resumed.kill();
  console.log("   Killed.");
}

main().catch(console.error);
