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

package e2bcloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// buildRecorder captures which endpoints TriggerBuild/GetBuildStatus hit and
// with what bodies, so tests can assert on the wire protocol.
type buildRecorder struct {
	createTemplateCalled bool
	v3Body               map[string]any
	v3TemplateID         string // what POST /v3/templates returns; defaults to "base"
	startBody            map[string]any
	startPath            string
	startCalled          bool
}

// templateBuildServer mimics the E2B Cloud template-build API surface:
//   - GET  /templates/{id}                            -> template metadata
//   - POST /v3/templates                              -> request a (re)build, returns templateID+buildID
//   - POST /v2/templates/{id}/builds/{buildID}        -> start the build
//   - GET  /templates/{id}/builds/{buildID}/status    -> build status
//   - POST /templates                                 -> legacy create (must NOT be used for rebuilds)
func templateBuildServer(t *testing.T, rec *buildRecorder, opts func(mux *http.ServeMux)) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			rec.createTemplateCalled = true
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{
				"templateID": "orphaned-new-template",
				"buildID":    "build-orphan",
				"status":     "building",
			})
		}
	})

	mux.HandleFunc("/templates/base", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"templateID": "base",
			"aliases":    []string{"base-name"},
			"cpuCount":   2,
			"memoryMB":   512,
			"public":     true,
			"ready":      true,
		})
	})

	mux.HandleFunc("/templates/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "template not found"})
	})

	mux.HandleFunc("/v3/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &rec.v3Body)
		templateID := rec.v3TemplateID
		if templateID == "" {
			templateID = "base"
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"templateID": templateID,
			"buildID":    "build-2",
			"public":     true,
			"aliases":    []string{"base-name"},
			"names":      []string{"base-name"},
			"tags":       []string{},
		})
	})

	recordStart := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rec.startCalled = true
		rec.startPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &rec.startBody)
		w.WriteHeader(http.StatusAccepted)
	}
	// Exact expected path, plus a catch-all under /v2/ so a start attempt
	// against ANY template/build path is recorded (longest pattern wins for
	// the exact path).
	mux.HandleFunc("/v2/templates/base/builds/build-2", recordStart)
	mux.HandleFunc("/v2/", recordStart)

	if opts != nil {
		opts(mux)
	}

	return httptest.NewServer(mux)
}

func newTestAdapter(ts *httptest.Server) *Adapter {
	client := NewClient(ClientConfig{Endpoint: ts.URL, APIKey: "test-key"})
	return NewAdapterWithClient("e2b-cloud", client, nil)
}

func TestE2BCloudAdapter_TriggerBuild_RebuildsExistingTemplate(t *testing.T) {
	rec := &buildRecorder{}
	ts := templateBuildServer(t, rec, nil)
	defer ts.Close()
	a := newTestAdapter(ts)

	build, err := a.TriggerBuild(context.Background(), "base", &adapter.BuildRequest{
		Dockerfile: "FROM ubuntu:22.04\nRUN echo hi",
		StartCmd:   "my-start",
	})
	if err != nil {
		t.Fatalf("TriggerBuild() error: %v", err)
	}

	if rec.createTemplateCalled {
		t.Error("TriggerBuild must not call POST /templates (creates an orphaned template)")
	}
	if rec.v3Body == nil {
		t.Fatal("expected POST /v3/templates to be called")
	}
	if got := rec.v3Body["name"]; got != "base-name" {
		t.Errorf("v3 name = %v, want %q", got, "base-name")
	}
	if got := rec.v3Body["cpuCount"]; got != float64(2) {
		t.Errorf("v3 cpuCount = %v, want 2", got)
	}
	if got := rec.v3Body["memoryMB"]; got != float64(512) {
		t.Errorf("v3 memoryMB = %v, want 512", got)
	}
	if !rec.startCalled {
		t.Fatal("expected POST /v2/templates/base/builds/build-2 to start the build")
	}
	if got := rec.startBody["fromImage"]; got != "ubuntu:22.04" {
		t.Errorf("start fromImage = %v, want %q", got, "ubuntu:22.04")
	}
	if got := rec.startBody["fromTemplate"]; got != nil {
		t.Errorf("start fromTemplate = %v, want absent", got)
	}
	if got := rec.startBody["startCmd"]; got != "my-start" {
		t.Errorf("start startCmd = %v, want %q", got, "my-start")
	}

	if build.TemplateID != "base" {
		t.Errorf("TemplateID = %q, want %q", build.TemplateID, "base")
	}
	if build.BuildID != "build-2" {
		t.Errorf("BuildID = %q, want %q", build.BuildID, "build-2")
	}
	if build.Status != adapter.BuildStatusBuilding {
		t.Errorf("Status = %q, want %q", build.Status, adapter.BuildStatusBuilding)
	}
}

func TestE2BCloudAdapter_TriggerBuild_NoDockerfileFallsBackToFromTemplate(t *testing.T) {
	rec := &buildRecorder{}
	ts := templateBuildServer(t, rec, nil)
	defer ts.Close()
	a := newTestAdapter(ts)

	if _, err := a.TriggerBuild(context.Background(), "base", &adapter.BuildRequest{}); err != nil {
		t.Fatalf("TriggerBuild() error: %v", err)
	}
	if got := rec.startBody["fromTemplate"]; got != "base" {
		t.Errorf("start fromTemplate = %v, want %q", got, "base")
	}
	if got := rec.startBody["fromImage"]; got != nil {
		t.Errorf("start fromImage = %v, want absent", got)
	}
}

func TestE2BCloudAdapter_TriggerBuild_TemplateNotFound(t *testing.T) {
	rec := &buildRecorder{}
	ts := templateBuildServer(t, rec, nil)
	defer ts.Close()
	a := newTestAdapter(ts)

	_, err := a.TriggerBuild(context.Background(), "missing", &adapter.BuildRequest{})
	if err == nil {
		t.Fatal("TriggerBuild() on missing template: expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsNotFound() {
		t.Errorf("expected 404 APIError passthrough, got %v", err)
	}
	if rec.v3Body != nil || rec.startCalled || rec.createTemplateCalled {
		t.Error("no build request must be made when the template does not exist")
	}
}

func TestE2BCloudAdapter_TriggerBuild_RejectsMismatchedTemplateID(t *testing.T) {
	// /v3/templates returns a DIFFERENT templateID than the one requested.
	rec := &buildRecorder{v3TemplateID: "some-other-template"}
	ts := templateBuildServer(t, rec, nil)
	defer ts.Close()
	a := newTestAdapter(ts)

	_, err := a.TriggerBuild(context.Background(), "base", &adapter.BuildRequest{})
	if err == nil {
		t.Fatal("expected error when the build request returns a different templateID")
	}
	// Any start attempt — including one against an unexpected template path —
	// is recorded by the /v2/ catch-all and must not happen.
	if rec.startCalled {
		t.Errorf("must not start a build for a mismatched templateID (start attempted at %s)", rec.startPath)
	}
	// The error must reflect the templateID mismatch, not a mock 404 or other
	// transport failure.
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		t.Errorf("expected a mismatch error, got APIError %d: %v", apiErr.StatusCode, err)
	}
	if !strings.Contains(err.Error(), "some-other-template") || !strings.Contains(err.Error(), `"base"`) {
		t.Errorf("error should name both template IDs, got: %v", err)
	}
}

func TestE2BCloudAdapter_TriggerBuild_MapsDockerfileInstructions(t *testing.T) {
	rec := &buildRecorder{}
	ts := templateBuildServer(t, rec, nil)
	defer ts.Close()
	a := newTestAdapter(ts)

	dockerfile := "# comment line\n" +
		"FROM --platform=linux/amd64 python:3.11\n" +
		"RUN apt-get update && \\\n" +
		"    apt-get install -y vim\n" +
		"ENV FOO=bar\n" +
		"WORKDIR /app\n" +
		"USER root\n"

	if _, err := a.TriggerBuild(context.Background(), "base", &adapter.BuildRequest{Dockerfile: dockerfile}); err != nil {
		t.Fatalf("TriggerBuild() error: %v", err)
	}
	if got := rec.startBody["fromImage"]; got != "python:3.11" {
		t.Errorf("fromImage = %v, want %q (--platform flag skipped)", got, "python:3.11")
	}
	steps, ok := rec.startBody["steps"].([]any)
	if !ok {
		t.Fatalf("steps missing or wrong type: %v", rec.startBody["steps"])
	}
	want := []struct {
		typ  string
		args []string
	}{
		{"RUN", []string{"apt-get update && apt-get install -y vim"}},
		{"ENV", []string{"FOO", "bar"}},
		{"WORKDIR", []string{"/app"}},
		{"USER", []string{"root"}},
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %v", len(steps), len(want), steps)
	}
	for i, w := range want {
		step, _ := steps[i].(map[string]any)
		if step["type"] != w.typ {
			t.Errorf("step[%d].type = %v, want %q", i, step["type"], w.typ)
		}
		args, _ := step["args"].([]any)
		if len(args) != len(w.args) {
			t.Errorf("step[%d].args = %v, want %v", i, args, w.args)
			continue
		}
		for j, a := range w.args {
			if args[j] != a {
				t.Errorf("step[%d].args[%d] = %v, want %q", i, j, args[j], a)
			}
		}
	}
}

func TestE2BCloudAdapter_TriggerBuild_RejectsUnsupportedDockerfile(t *testing.T) {
	cases := []struct {
		name       string
		dockerfile string
	}{
		{"COPY needs file upload", "FROM base\nCOPY app /app"},
		{"ADD needs file upload", "FROM base\nADD app /app"},
		{"multi-stage", "FROM golang:1.22 AS build\nRUN go build\nFROM base\nCOPY --from=build /app /app"},
		{"CMD has no step type", "FROM base\nCMD [\"/bin/sh\"]"},
		{"ENTRYPOINT has no step type", "FROM base\nENTRYPOINT /bin/sh"},
		{"EXPOSE has no step type", "FROM base\nEXPOSE 8080"},
		{"ARG has no step type", "ARG VERSION=1\nFROM base"},
		{"RUN before FROM", "RUN echo hi\nFROM base"},
		{"unknown instruction", "FROM base\nFROBNICATE now"},
		{"RUN --mount unsupported", "FROM base\nRUN --mount=type=cache,target=/x make"},
		{"no FROM", "RUN echo hi"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &buildRecorder{}
			ts := templateBuildServer(t, rec, nil)
			defer ts.Close()
			a := newTestAdapter(ts)

			_, err := a.TriggerBuild(context.Background(), "base", &adapter.BuildRequest{Dockerfile: tc.dockerfile})
			if err == nil {
				t.Fatal("expected rejection for unsupported Dockerfile")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 APIError, got %v", err)
			}
			// Rejection must happen BEFORE any upstream build registration.
			if rec.v3Body != nil || rec.startCalled || rec.createTemplateCalled {
				t.Error("no upstream request must be made for a rejected Dockerfile")
			}
		})
	}
}

func TestE2BCloudAdapter_GetBuildStatus_UsesGETAndDecodesRealResponse(t *testing.T) {
	ts := templateBuildServer(t, &buildRecorder{}, func(mux *http.ServeMux) {
		mux.HandleFunc("/templates/base/builds/build-2/status", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			// Real E2B TemplateBuildInfo shape: logs is an ARRAY and entries are structured.
			json.NewEncoder(w).Encode(map[string]any{
				"templateID": "base",
				"buildID":    "build-2",
				"status":     "ready",
				"logs":       []string{},
				"logEntries": []map[string]any{
					{"timestamp": "2026-11-18T10:00:00Z", "level": "info", "message": "step 1 done"},
					{"timestamp": "2026-11-18T10:00:01Z", "level": "info", "message": "build finished"},
				},
			})
		})
	})
	defer ts.Close()
	a := newTestAdapter(ts)

	status, err := a.GetBuildStatus(context.Background(), "base", "build-2")
	if err != nil {
		t.Fatalf("GetBuildStatus() error: %v", err)
	}
	if status.BuildID != "build-2" {
		t.Errorf("BuildID = %q, want %q", status.BuildID, "build-2")
	}
	if status.Status != adapter.BuildStatusReady {
		t.Errorf("Status = %q, want %q", status.Status, adapter.BuildStatusReady)
	}
	if status.Logs != "step 1 done\nbuild finished" {
		t.Errorf("Logs = %q, want joined log entry messages", status.Logs)
	}
	if status.Error != "" {
		t.Errorf("Error = %q, want empty", status.Error)
	}
}

func TestE2BCloudAdapter_GetBuildStatus_MapsFailureReason(t *testing.T) {
	ts := templateBuildServer(t, &buildRecorder{}, func(mux *http.ServeMux) {
		mux.HandleFunc("/templates/base/builds/build-2/status", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"templateID": "base",
				"buildID":    "build-2",
				"status":     "error",
				"logs":       []string{},
				"reason":     map[string]any{"message": "dockerfile build failed"},
			})
		})
	})
	defer ts.Close()
	a := newTestAdapter(ts)

	status, err := a.GetBuildStatus(context.Background(), "base", "build-2")
	if err != nil {
		t.Fatalf("GetBuildStatus() error: %v", err)
	}
	if status.Status != adapter.BuildStatusError {
		t.Errorf("Status = %q, want %q", status.Status, adapter.BuildStatusError)
	}
	if status.Error != "dockerfile build failed" {
		t.Errorf("Error = %q, want reason message", status.Error)
	}
}

func TestE2BCloudAdapter_GetBuildStatus_NormalizesWaitingToBuilding(t *testing.T) {
	ts := templateBuildServer(t, &buildRecorder{}, func(mux *http.ServeMux) {
		mux.HandleFunc("/templates/base/builds/build-2/status", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"templateID": "base",
				"buildID":    "build-2",
				"status":     "waiting",
				"logs":       []string{},
			})
		})
	})
	defer ts.Close()
	a := newTestAdapter(ts)

	status, err := a.GetBuildStatus(context.Background(), "base", "build-2")
	if err != nil {
		t.Fatalf("GetBuildStatus() error: %v", err)
	}
	if status.Status != adapter.BuildStatusBuilding {
		t.Errorf("Status = %q, want %q (waiting normalized)", status.Status, adapter.BuildStatusBuilding)
	}
}
