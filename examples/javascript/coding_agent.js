/**
 * Coding Agent - Run code analysis and tests in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node coding_agent.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Set up environment
    console.log("1. Setting up dev environment...");
    await sandbox.commands.run("pip install pytest");

    // 2. Create project structure
    console.log("2. Creating project...");
    await sandbox.commands.run("mkdir -p /tmp/project/src /tmp/project/tests");

    await sandbox.files.write(
      "/tmp/project/src/calculator.py",
      `
class Calculator:
    def add(self, a, b):
        return a + b

    def subtract(self, a, b):
        return a - b

    def multiply(self, a, b):
        return a * b

    def divide(self, a, b):
        if b == 0:
            raise ValueError("Cannot divide by zero")
        return a / b
`
    );

    await sandbox.files.write(
      "/tmp/project/tests/test_calculator.py",
      `
import sys
sys.path.insert(0, '/tmp/project/src')
from calculator import Calculator

def test_add():
    c = Calculator()
    assert c.add(2, 3) == 5

def test_subtract():
    c = Calculator()
    assert c.subtract(5, 3) == 2

def test_multiply():
    c = Calculator()
    assert c.multiply(3, 4) == 12

def test_divide():
    c = Calculator()
    assert c.divide(10, 2) == 5.0
`
    );

    // 3. Run tests
    console.log("3. Running tests...");
    const result = await sandbox.commands.run("python -m pytest tests/ -v", {
      cwd: "/tmp/project",
    });
    console.log(`   Output:\n${result.stdout}`);
    console.log(`   Exit code: ${result.exitCode}`);

    // 4. Collect results
    console.log("4. Collecting results...");
    const execution = await sandbox.runCode(`
import json
report = {
    "project": "calculator",
    "status": "passed" if ${result.exitCode} == 0 else "failed",
    "exit_code": ${result.exitCode},
}
with open("/tmp/project/report.json", "w") as f:
    json.dump(report, f, indent=2)
print("Report generated")
`);
    for (const log of execution.logs) {
      console.log(`   ${log}`);
    }

    const report = await sandbox.files.read("/tmp/project/report.json");
    console.log(`   Report: ${report}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
