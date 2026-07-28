// Package main demonstrates filesystem operations via Go.
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

type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

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

	// Create sandbox first
	fmt.Println("0. Creating sandbox...")
	sbxID := createSandbox(client, baseURL, apiKey)
	fmt.Printf("   Sandbox: %s\n", sbxID)

	// 1. Write file
	fmt.Println("1. Writing file...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/filesystem/upload", sbxID),
		map[string]string{"path": "/tmp/hello.txt", "content": "Hello from E2BGateway!"})
	fmt.Println("   Written: /tmp/hello.txt")

	// 2. Read file
	fmt.Println("2. Reading file...")
	req, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/sandboxes/%s/filesystem/download?path=/tmp/hello.txt", baseURL, sbxID), nil)
	req.Header.Set("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	fmt.Printf("   Content: %s\n", string(body))

	// 3. List directory
	fmt.Println("3. Listing /tmp directory...")
	files := post[[]FileEntry](client, baseURL, apiKey,
		fmt.Sprintf("/sandboxes/%s/filesystem/list", sbxID),
		map[string]string{"path": "/tmp"})
	for _, f := range files {
		typeStr := "file"
		if f.IsDir {
			typeStr = "dir"
		}
		fmt.Printf("   - %s (%s)\n", f.Name, typeStr)
	}

	// 4. Create directory
	fmt.Println("4. Creating directory...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/filesystem/mkdir", sbxID),
		map[string]string{"path": "/tmp/my_project"})
	fmt.Println("   Created: /tmp/my_project")

	// 5. Remove file
	fmt.Println("5. Removing file...")
	doJSON(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/filesystem/rm", sbxID),
		map[string]string{"path": "/tmp/hello.txt"})
	fmt.Println("   Removed: /tmp/hello.txt")

	// Kill sandbox
	fmt.Println("6. Killing sandbox...")
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/sandboxes/%s", baseURL, sbxID), nil)
	req.Header.Set("X-API-Key", apiKey)
	killResp, killErr := client.Do(req)
	if killErr != nil {
		panic(killErr)
	}
	_ = killResp.Body.Close()
	fmt.Println("   Killed.")
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
		SandboxID string `json:"sandboxID"`
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

func post[T any](client *http.Client, baseURL, apiKey, path string, body interface{}) T {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", baseURL+path, bytes.NewReader(b))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result T
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result
}
