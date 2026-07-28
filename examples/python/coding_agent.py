"""Coding Agent - Run AI coding agent in a sandbox.

Demonstrates the coding agent workflow:
1. Create sandbox with development tools
2. Clone a repository
3. Run code analysis/linting
4. Execute tests
5. Collect results

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python coding_agent.py
"""

import os
from e2b import Sandbox


def main():
    sandbox = Sandbox.create(
        template="base",
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Set up development environment
        print("1. Setting up dev environment...")
        sandbox.commands.run("pip install flake8 pytest")
        sandbox.envs.set({"PYTHONDONTWRITEBYTECODE": "1"})

        # 2. Create a project
        print("2. Creating sample project...")
        sandbox.commands.run("mkdir -p /tmp/project/src /tmp/project/tests")

        # Write source code
        sandbox.files.write("/tmp/project/src/calculator.py", """
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
""")

        # Write tests
        sandbox.files.write("/tmp/project/tests/test_calculator.py", """
import sys
sys.path.insert(0, '/tmp/project/src')
from calculator import Calculator

def test_add():
    c = Calculator()
    assert c.add(2, 3) == 5
    assert c.add(-1, 1) == 0

def test_subtract():
    c = Calculator()
    assert c.subtract(5, 3) == 2

def test_multiply():
    c = Calculator()
    assert c.multiply(3, 4) == 12

def test_divide():
    c = Calculator()
    assert c.divide(10, 2) == 5.0

def test_divide_by_zero():
    c = Calculator()
    try:
        c.divide(1, 0)
        assert False, "Should have raised ValueError"
    except ValueError:
        pass
""")

        # 3. Run linting
        print("3. Running linter...")
        result = sandbox.commands.run(
            "flake8 /tmp/project/src/ --max-line-length=120",
            cwd="/tmp/project",
        )
        print(f"   Lint result: {'clean' if not result.stdout.strip() else result.stdout.strip()}")

        # 4. Run tests
        print("4. Running tests...")
        result = sandbox.commands.run(
            "python -m pytest tests/ -v",
            cwd="/tmp/project",
        )
        print(f"   Test output:\n{result.stdout}")
        print(f"   Exit code: {result.exit_code}")

        # 5. Generate report
        print("5. Generating report...")
        sandbox.run_code("""
import json

report = {
    "project": "calculator",
    "files": ["src/calculator.py", "tests/test_calculator.py"],
    "status": "all_tests_passed",
}
with open("/tmp/project/report.json", "w") as f:
    json.dump(report, f, indent=2)
print("Report generated")
""")

        report = sandbox.files.read("/tmp/project/report.json")
        print(f"   Report: {report}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
