/**
 * Filesystem Operations - File CRUD in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node filesystem.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Write a file
    console.log("1. Writing file...");
    await sandbox.files.write("/tmp/hello.txt", "Hello from E2BGateway!");
    console.log("   Written: /tmp/hello.txt");

    // 2. Read a file
    console.log("2. Reading file...");
    const content = await sandbox.files.read("/tmp/hello.txt");
    console.log(`   Content: ${content}`);

    // 3. List directory
    console.log("3. Listing /tmp directory...");
    const entries = await sandbox.files.list("/tmp");
    for (const entry of entries) {
      console.log(`   - ${entry.name} (${entry.type})`);
    }

    // 4. Create directory
    console.log("4. Creating directory...");
    await sandbox.files.makeDir("/tmp/my_project");
    console.log("   Created: /tmp/my_project");

    // 5. Write multiple files
    console.log("5. Writing multiple files...");
    await sandbox.files.write(
      "/tmp/my_project/main.py",
      "print('hello')"
    );
    await sandbox.files.write(
      "/tmp/my_project/config.json",
      '{"key": "value"}'
    );
    const files = await sandbox.files.list("/tmp/my_project");
    for (const f of files) {
      console.log(`   - ${f.name}`);
    }

    // 6. Move/rename file
    console.log("6. Moving file...");
    await sandbox.files.move("/tmp/my_project/main.py", "/tmp/my_project/app.py");
    const afterMove = await sandbox.files.list("/tmp/my_project");
    console.log(
      `   Files after move: ${afterMove.map((f) => f.name).join(", ")}`
    );

    // 7. Remove files
    console.log("7. Removing files...");
    await sandbox.files.remove("/tmp/my_project/app.py");
    await sandbox.files.remove("/tmp/my_project/config.json");
    console.log("   Removed files from /tmp/my_project");
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
