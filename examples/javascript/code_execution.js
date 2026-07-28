/**
 * Code Execution - Execute Python and JavaScript code in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node code_execution.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Execute Python code
    console.log("1. Executing Python code...");
    const execution1 = await sandbox.runCode(`
import sys
print(f"Python {sys.version}")
result = sum(range(1, 101))
print(f"Sum of 1-100: {result}")
`);
    console.log(`   Output: ${execution1.logs}`);
    if (execution1.error) {
      console.log(`   Error: ${execution1.error}`);
    }

    // 2. Execute code with variables (persistent state)
    console.log("2. Executing code with variables...");
    await sandbox.runCode("my_var = 42");
    const execution2 = await sandbox.runCode(
      "print(f'The answer is: {my_var}')"
    );
    console.log(`   Output: ${execution2.logs}`);

    // 3. Execute JavaScript code
    console.log("3. Executing JavaScript code...");
    const execution3 = await sandbox.runCode(
      `console.log('Hello from JS!');
const sum = [1,2,3].reduce((a,b)=>a+b,0);
console.log(\`Sum: \${sum}\`);`,
      { language: "javascript" }
    );
    console.log(`   Output: ${execution3.logs}`);

    // 4. Handle execution errors
    console.log("4. Testing error handling...");
    const execution4 = await sandbox.runCode(
      "raise ValueError('test error')"
    );
    if (execution4.error) {
      console.log(`   Caught expected error: ${execution4.error.name}`);
    }

    // 5. Code with file I/O
    console.log("5. Code with file operations...");
    const execution5 = await sandbox.runCode(`
with open('/tmp/test.txt', 'w') as f:
    f.write('Hello from sandbox!')
with open('/tmp/test.txt', 'r') as f:
    print(f.read())
`);
    console.log(`   Output: ${execution5.logs}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
