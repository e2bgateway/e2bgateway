/**
 * Commands - Run shell commands with streaming output.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node commands.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Simple command
    console.log("1. Running simple command...");
    const result1 = await sandbox.commands.run("uname -a");
    console.log(`   Output: ${result1.stdout.trim()}`);

    // 2. Command with arguments
    console.log("2. Running command with arguments...");
    const result2 = await sandbox.commands.run("ls -la /tmp");
    console.log(`   Output:\n${result2.stdout}`);

    // 3. Environment variables
    console.log("3. Running command with env vars...");
    await sandbox.envs.set({ MY_VAR: "hello", MY_NUM: "42" });
    const result3 = await sandbox.commands.run("echo $MY_VAR $MY_NUM");
    console.log(`   Output: ${result3.stdout.trim()}`);

    // 4. Command with working directory
    console.log("4. Running command in specific directory...");
    await sandbox.commands.run("mkdir -p /tmp/workdir");
    const result4 = await sandbox.commands.run("pwd", { cwd: "/tmp/workdir" });
    console.log(`   Working dir: ${result4.stdout.trim()}`);

    // 5. Streaming output
    console.log("5. Streaming command output...");
    const result5 = await sandbox.commands.run(
      "for i in 1 2 3 4 5; do echo \"Line $i\"; sleep 0.1; done",
      {
        onStdout: (data) => process.stdout.write(`   [stdout] ${data}`),
        onStderr: (data) => process.stdout.write(`   [stderr] ${data}`),
      }
    );

    // 6. Command exit code
    console.log("\n6. Checking exit codes...");
    const result6a = await sandbox.commands.run("exit 0");
    console.log(`   exit 0 -> exit_code: ${result6a.exitCode}`);
    const result6b = await sandbox.commands.run("exit 1", { check: false });
    console.log(`   exit 1 -> exit_code: ${result6b.exitCode}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
