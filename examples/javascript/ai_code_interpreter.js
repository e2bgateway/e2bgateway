/**
 * AI Code Interpreter - Interactive coding session with persistent state.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node ai_code_interpreter.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Install packages
    console.log("1. Installing packages...");
    await sandbox.runCode(`
import subprocess
subprocess.run(["pip", "install", "numpy"], capture_output=True)
print("numpy installed")
`);

    // 2. Data analysis with numpy
    console.log("2. Running data analysis...");
    const exec1 = await sandbox.runCode(`
import numpy as np

data = np.random.randn(1000)
print(f"Generated {len(data)} data points")
print(f"Mean: {np.mean(data):.4f}")
print(f"Std:  {np.std(data):.4f}")
print(f"Min:  {np.min(data):.4f}")
print(f"Max:  {np.max(data):.4f}")
`);
    for (const log of exec1.logs) {
      console.log(`   ${log}`);
    }

    // 3. Persistent state across executions
    console.log("3. Using persistent state...");
    await sandbox.runCode(`
import numpy as np
my_dataset = np.random.randn(100, 5)
print(f"Created dataset with shape {my_dataset.shape}")
`);
    const exec2 = await sandbox.runCode(`
column_means = my_dataset.mean(axis=0)
print(f"Column means: {column_means}")
`);
    for (const log of exec2.logs) {
      console.log(`   ${log}`);
    }

    // 4. Export results
    console.log("4. Exporting results...");
    await sandbox.runCode(`
import json
results = {
    "shape": list(my_dataset.shape),
    "column_means": column_means.tolist(),
}
with open("/tmp/analysis_results.json", "w") as f:
    json.dump(results, f, indent=2)
print("Results saved")
`);
    const content = await sandbox.files.read("/tmp/analysis_results.json");
    console.log(`   Exported: ${content.substring(0, 200)}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
