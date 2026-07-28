/**
 * Data Analysis - Process and analyze data in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node data_analysis.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Setup
    console.log("1. Setting up environment...");
    await sandbox.runCode(`
import subprocess
subprocess.run(["pip", "install", "pandas"], capture_output=True)
print("Ready")
`);

    // 2. Generate and analyze data
    console.log("2. Generating and analyzing data...");
    const exec1 = await sandbox.runCode(`
import pandas as pd
import numpy as np

np.random.seed(42)
df = pd.DataFrame({
    "name": [f"user_{i}" for i in range(100)],
    "score": np.random.randint(0, 100, 100),
    "category": np.random.choice(["A", "B", "C"], 100),
})

print("=== Summary Statistics ===")
print(df.describe().to_string())

print("\\n=== By Category ===")
grouped = df.groupby("category")["score"].agg(["mean", "min", "max", "count"])
print(grouped.to_string())
`);
    for (const log of exec1.logs) {
      console.log(`   ${log}`);
    }

    // 3. Export
    console.log("3. Exporting results...");
    await sandbox.runCode(`
import json
result = {
    "total_records": len(df),
    "mean_score": float(df["score"].mean()),
}
with open("/tmp/analysis.json", "w") as f:
    json.dump(result, f, indent=2)
print(f"Exported to /tmp/analysis.json")
`);
    const content = await sandbox.files.read("/tmp/analysis.json");
    const data = JSON.parse(content);
    console.log(`   Total records: ${data.total_records}`);
    console.log(`   Mean score: ${data.mean_score.toFixed(2)}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
