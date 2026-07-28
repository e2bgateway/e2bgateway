// Package main demonstrates sandbox lifecycle management via Go.
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

type SandboxCreateRequest struct {
	TemplateID string `json:"templateID"`
	Timeout    int    `json:"timeout,omitempty"`
}

type SandboxCreateResponse struct {
	SandboxID       string `json:"sandboxID"`
	TemplateID      string `json:"templateID"`
	ClientID        string `json:"clientID,omitempty"`
	EnvdVersion     string `json:"envdVersion,omitempty"`
	EnvdAccessToken string `json:"envdAccessToken,omitempty"`
}

type SandboxInfo struct {
	SandboxID  string `json:"sandboxID"`
	TemplateID string `json:"templateID"`
	State      string `json:"state"`
}

type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
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

	// 1. Create sandbox
	fmt.Println("1. Creating sandbox...")
	createResp := post[SandboxCreateResponse](client, baseURL, apiKey, "/sandboxes",
		SandboxCreateRequest{TemplateID: "base", Timeout: 300})
	fmt.Printf("   Created: %s (envdVersion: %s)\n", createResp.SandboxID, createResp.EnvdVersion)

	// 2. List sandboxes
	fmt.Println("2. Listing sandboxes...")
	sandboxes := get[[]SandboxInfo](client, baseURL, apiKey, "/sandboxes")
	for _, sbx := range sandboxes {
		fmt.Printf("   - %s (state: %s)\n", sbx.SandboxID, sbx.State)
	}

	// 3. Get sandbox
	fmt.Println("3. Getting sandbox info...")
	sbxInfo := get[SandboxInfo](client, baseURL, apiKey, fmt.Sprintf("/sandboxes/%s", createResp.SandboxID))
	fmt.Printf("   ID: %s, State: %s, Template: %s\n", sbxInfo.SandboxID, sbxInfo.State, sbxInfo.TemplateID)

	// 4. Pause sandbox
	fmt.Println("4. Pausing sandbox...")
	doRequest(client, baseURL, apiKey, "POST", fmt.Sprintf("/sandboxes/%s/pause", createResp.SandboxID), nil)
	fmt.Println("   Paused.")

	// 5. Resume sandbox
	fmt.Println("5. Resuming sandbox...")
	resumeResp := post[SandboxCreateResponse](client, baseURL, apiKey,
		fmt.Sprintf("/sandboxes/%s/resume", createResp.SandboxID), nil)
	fmt.Printf("   Resumed: %s\n", resumeResp.SandboxID)

	// 6. Set timeout
	fmt.Println("6. Setting timeout...")
	doRequest(client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/timeout", createResp.SandboxID),
		map[string]int{"timeout": 600})
	fmt.Println("   Timeout set to 600s.")

	// 7. Kill sandbox
	fmt.Println("7. Killing sandbox...")
	doRequest(client, baseURL, apiKey, "DELETE", fmt.Sprintf("/sandboxes/%s", createResp.SandboxID), nil)
	fmt.Println("   Killed.")
}

func post[T any](client *http.Client, baseURL, apiKey, path string, body interface{}) T {
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest("POST", baseURL+path, bodyReader)
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		panic(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b)))
	}
	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		panic(fmt.Sprintf("decode error: %v", err))
	}
	return result
}

func get[T any](client *http.Client, baseURL, apiKey, path string) T {
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		panic(fmt.Sprintf("decode error: %v", err))
	}
	return result
}

func doRequest(client *http.Client, baseURL, apiKey, method, path string, body interface{}) {
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, bodyReader)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	_ = resp.Body.Close()
}
