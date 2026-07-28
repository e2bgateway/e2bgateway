"""Code Execution - Execute Python and JavaScript code in a sandbox.

Demonstrates the CodeInterpreter for interactive code execution:
1. Create a code interpreter sandbox
2. Execute Python code
3. Execute JavaScript code
4. Handle execution results and errors
5. Stream execution logs

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python code_execution.py
"""

import os
from e2b_code_interpreter import Sandbox


def main():
    sandbox = Sandbox.create(
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Execute Python code
        print("1. Executing Python code...")
        execution = sandbox.run_code("""
import sys
print(f"Python {sys.version}")
result = sum(range(1, 101))
print(f"Sum of 1-100: {result}")
""")
        print(f"   Output: {execution.logs}")
        if execution.error:
            print(f"   Error: {execution.error}")

        # 2. Execute code with variables
        print("2. Executing code with variables...")
        sandbox.run_code("my_var = 42")
        execution = sandbox.run_code("print(f'The answer is: {my_var}')")
        print(f"   Output: {execution.logs}")

        # 3. Execute JavaScript code
        print("3. Executing JavaScript code...")
        execution = sandbox.run_code(
            "console.log('Hello from JS!'); const sum = [1,2,3].reduce((a,b)=>a+b,0); console.log(`Sum: ${sum}`);",
            language="javascript",
        )
        print(f"   Output: {execution.logs}")

        # 4. Handle execution errors
        print("4. Testing error handling...")
        execution = sandbox.run_code("raise ValueError('test error')")
        if execution.error:
            print(f"   Caught expected error: {execution.error.name}")

        # 5. Execute code with file I/O
        print("5. Code with file operations...")
        execution = sandbox.run_code("""
with open('/tmp/test.txt', 'w') as f:
    f.write('Hello from sandbox!')
with open('/tmp/test.txt', 'r') as f:
    print(f.read())
""")
        print(f"   Output: {execution.logs}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
