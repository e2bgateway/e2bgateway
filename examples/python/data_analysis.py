"""Data Analysis - Process and analyze data in a sandbox.

Demonstrates data analysis workflow:
1. Generate sample data
2. Process with Python
3. Create summary statistics
4. Export results

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python data_analysis.py
"""

import os
import json
from e2b_code_interpreter import Sandbox


def main():
    sandbox = Sandbox.create(
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Install dependencies
        print("1. Setting up environment...")
        sandbox.run_code("""
import subprocess
subprocess.run(["pip", "install", "pandas"], capture_output=True)
print("Ready")
""")

        # 2. Generate and analyze data
        print("2. Generating and analyzing data...")
        execution = sandbox.run_code("""
import pandas as pd
import numpy as np

# Generate sample dataset
np.random.seed(42)
df = pd.DataFrame({
    "name": [f"user_{i}" for i in range(100)],
    "score": np.random.randint(0, 100, 100),
    "category": np.random.choice(["A", "B", "C"], 100),
})

# Summary statistics
print("=== Summary Statistics ===")
print(df.describe().to_string())

# Group by category
print("\\n=== By Category ===")
grouped = df.groupby("category")["score"].agg(["mean", "min", "max", "count"])
print(grouped.to_string())
""")
        for log in execution.logs:
            print(f"   {log}")

        # 3. Export to JSON
        print("3. Exporting results...")
        sandbox.run_code("""
import json
result = {
    "total_records": len(df),
    "mean_score": float(df["score"].mean()),
    "by_category": grouped.reset_index().to_dict(orient="records"),
}
with open("/tmp/analysis.json", "w") as f:
    json.dump(result, f, indent=2)
print(f"Exported to /tmp/analysis.json")
""")

        # 4. Read the exported file
        content = sandbox.files.read("/tmp/analysis.json")
        data = json.loads(content)
        print(f"   Total records: {data['total_records']}")
        print(f"   Mean score: {data['mean_score']:.2f}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
