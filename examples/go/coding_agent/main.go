// Package main demonstrates a coding agent workflow: create project,
// write code, run tests, and collect results in a sandbox.
//
// Usage:
//
//	E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key go run main.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	baseURL := os.Getenv("E2B_DOMAIN")
	if baseURL == "" {
		baseURL = "localhost:8080"
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		apiKey = "test-key"
	}
	if baseURL[:4] != "http" {
		baseURL = "http://" + baseURL
	}
	client := &http.Client{}

	// Create sandbox
	fmt.Println("0. Creating sandbox...")
	sbxID := createSandbox(client, baseURL, apiKey)
	fmt.Printf("   Sandbox: %s\n", sbxID)

	// Create project
	fmt.Println("1. Creating project structure...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/commands", sbxID),
		map[string]string{"cmd": "mkdir -p /tmp/project"})

	// Write source code
	fmt.Println("2. Writing source code...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/filesystem/upload", sbxID),
		map[string]string{
			"path":    "/tmp/project/calculator.py",
			"content": "def add(a, b): return a + b\ndef multiply(a, b): return a * b\n",
		})

	// Write test
	fmt.Println("3. Writing tests...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/filesystem/upload", sbxID),
		map[string]string{
			"path": "/tmp/project/test_calc.py",
			"content": `
import sys; sys.path.insert(0, '/tmp/project')
from calculator import add, multiply
assert add(2, 3) == 5
assert multiply(3, 4) == 12
print("All tests passed!")
`,
		})

	// Run tests
	fmt.Println("4. Running tests...")
	resp := postJSON(client, baseURL, apiKey,
		fmt.Sprintf("/sandboxes/%s/commands", sbxID),
		map[string]string{"cmd": "cd /tmp/project && python3 test_calc.py"})
	fmt.Printf("   Output: %s\n", resp["stdout"])
	fmt.Printf("   Exit code: %v\n", resp["exitCode"])

	// Cleanup
	fmt.Println("5. Killing sandbox...")
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/sandboxes/%s", baseURL, sbxID), nil)
	req.Header.Set("X-API-Key", apiKey)
	r, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	_ = r.Body.Close()
	fmt.Println("   Done.")
}

func createSandbox(client *http.Client, baseURL, apiKey string) string {
	b, _ := json.Marshal(map[string]string{"templateID": "base"})
	req, _ := http.NewRequest("POST", baseURL+"/sandboxes", bytes.NewReader(b))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		SandboxID string `json:"sandboxId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.SandboxID
}

func doJSON(client *http.Client, baseURL, apiKey, method, path string, body interface{}) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, baseURL+path, bytes.NewReader(b))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	_ = resp.Body.Close()
}

func postJSON(client *http.Client, baseURL, apiKey, path string, body interface{}) map[string]interface{} {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", baseURL+path, bytes.NewReader(b))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)
	return result
}
