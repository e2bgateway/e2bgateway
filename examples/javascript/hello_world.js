/**
 * Hello World - Create a sandbox and run a simple command.
 *
 * Demonstrates basic E2B JavaScript SDK usage:
 * 1. Create a new sandbox
 * 2. Run a command
 * 3. Print output
 * 4. Kill the sandbox
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node hello_world.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // Run a simple command
    const result = await sandbox.commands.run("echo 'Hello from E2BGateway!'");
    console.log(`Output: ${result.stdout.trim()}`);

    // Get sandbox info
    console.log(`Sandbox ID: ${sandbox.sandboxId}`);
  } finally {
    await sandbox.kill();
    console.log("Sandbox killed.");
  }
}

main().catch(console.error);
