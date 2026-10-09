// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// SandboxCreateRequest is the E2B-compatible sandbox creation request.
type SandboxCreateRequest struct {
	TemplateID string `json:"templateID"`
	Timeout    int    `json:"timeout,omitempty"`
}

// SandboxCreateResponse is the E2B-compatible sandbox creation response.
type SandboxCreateResponse struct {
	SandboxID       string `json:"sandboxID"`
	TemplateID      string `json:"templateID"`
	ClientID        string `json:"clientID,omitempty"`
	EnvdVersion     string `json:"envdVersion,omitempty"`
	EnvdAccessToken string `json:"envdAccessToken,omitempty"`
}

// SandboxInfo represents a sandbox in list/get responses.
type SandboxInfo struct {
	SandboxID  string `json:"sandboxID"`
	TemplateID string `json:"templateID"`
	State      string `json:"state"`
}

// CommandRequest is a command execution request.
type CommandRequest struct {
	Command string `json:"command"`
}

// CommandResult is a command execution result.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
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

	// Ensure URL scheme
	if baseURL[:4] != "http" {
		baseURL = "http://" + baseURL
	}

	client := &http.Client{}

	// 1. Create sandbox
	fmt.Println("1. Creating sandbox...")
	createResp := doRequest[SandboxCreateResponse](client, baseURL, apiKey, "POST", "/sandboxes",
		SandboxCreateRequest{TemplateID: "base"})
	fmt.Printf("   Created: %s\n", createResp.SandboxID)

	// 2. Run command
	fmt.Println("2. Running command...")
	cmdResp := doRequest[CommandResult](client, baseURL, apiKey, "POST",
		fmt.Sprintf("/sandboxes/%s/commands", createResp.SandboxID),
		CommandRequest{Command: "echo 'Hello from E2BGateway Go SDK!'"})
	fmt.Printf("   Output: %s\n", cmdResp.Stdout)

	// 3. List sandboxes
	fmt.Println("3. Listing sandboxes...")
	listResp := doRequest[[]SandboxInfo](client, baseURL, apiKey, "GET", "/sandboxes", nil)
	fmt.Printf("   Running sandboxes: %d\n", len(listResp))

	// 4. Kill sandbox
	fmt.Println("4. Killing sandbox...")
	doRequestRaw(client, baseURL, apiKey, "DELETE",
		fmt.Sprintf("/sandboxes/%s", createResp.SandboxID), nil)
	fmt.Println("   Killed.")
}

func doRequest[T any](client *http.Client, baseURL, apiKey, method, path string, body any) T {
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		panic(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)))
	}

	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		panic(fmt.Sprintf("decode error: %v", err))
	}
	return result
}

func doRequestRaw(client *http.Client, baseURL, apiKey, method, path string, body any) {
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
	defer func() { _ = resp.Body.Close() }()
}
