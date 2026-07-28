"""AI Code Interpreter - Interactive coding session with persistent state.

Demonstrates an AI-like code interpreter workflow:
1. Create a code interpreter sandbox
2. Install packages
3. Run data analysis code
4. Generate visualizations
5. Export results

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python ai_code_interpreter.py
"""

import os
from e2b_code_interpreter import Sandbox


def main():
    sandbox = Sandbox.create(
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Install packages
        print("1. Installing packages...")
        sandbox.run_code("""
import subprocess
subprocess.run(["pip", "install", "numpy"], capture_output=True)
print("numpy installed")
""")

        # 2. Data analysis
        print("2. Running data analysis...")
        execution = sandbox.run_code("""
import numpy as np

# Generate sample data
data = np.random.randn(1000)
print(f"Generated {len(data)} data points")
print(f"Mean: {np.mean(data):.4f}")
print(f"Std:  {np.std(data):.4f}")
print(f"Min:  {np.min(data):.4f}")
print(f"Max:  {np.max(data):.4f}")
""")
        for log in execution.logs:
            print(f"   {log}")

        # 3. Persistent state across executions
        print("3. Using persistent state...")
        sandbox.run_code("""
import numpy as np
my_dataset = np.random.randn(100, 5)
print(f"Created dataset with shape {my_dataset.shape}")
""")
        execution = sandbox.run_code("""
# Access variable from previous execution
column_means = my_dataset.mean(axis=0)
print(f"Column means: {column_means}")
""")
        for log in execution.logs:
            print(f"   {log}")

        # 4. Export results to file
        print("4. Exporting results...")
        sandbox.run_code("""
import json
results = {
    "shape": list(my_dataset.shape),
    "column_means": column_means.tolist(),
}
with open("/tmp/analysis_results.json", "w") as f:
    json.dump(results, f, indent=2)
print("Results saved to /tmp/analysis_results.json")
""")

        # 5. Read exported file
        content = sandbox.files.read("/tmp/analysis_results.json")
        print(f"   Exported results: {content[:200]}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
