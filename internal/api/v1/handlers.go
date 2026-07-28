// Package v1 implements the E2B API v1 HTTP handlers.
package v1

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
	"github.com/e2bgateway/e2bgateway/internal/routing"
)

// --- Sandbox Handlers ---

// CreateSandboxHandler handles POST /sandboxes
func CreateSandboxHandler(registry *adapter.Registry, router *routing.Router, envdDomain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dtoReq dto.SandboxCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		// Select backend
		backendName, err := router.SelectBackend(r.Context(), &routing.RoutingRequest{
			TemplateID: dtoReq.TemplateID,
		})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}

		a, ok := registry.Get(backendName)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "Backend "+backendName+" not found")
			return
		}

		// Convert DTO to adapter request
		req := &adapter.CreateSandboxRequest{
			TemplateID: dtoReq.TemplateID,
			Alias:      dtoReq.Alias,
			Timeout:    dtoReq.Timeout,
			Metadata:   dtoReq.Metadata,
		}

		sandbox, err := a.CreateSandbox(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Generate envd access token for direct sandbox connections
		envdAccessToken := generateEnvdToken(sandbox.SandboxID)

		resp := dto.SandboxCreateResponse{
			SandboxID:          sandbox.SandboxID,
			TemplateID:         sandbox.TemplateID,
			Alias:              sandbox.Alias,
			ClientID:           sandbox.ClientID,
			EnvdVersion:        "0.1.0",
			EnvdAccessToken:    envdAccessToken,
			SandboxDomain:      envdDomain,
		}

		writeJSON(w, http.StatusCreated, resp)
	}
}

// ListSandboxesHandler handles GET /sandboxes
func ListSandboxesHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := adapter.ListOptions{}
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil {
				opts.Limit = limit
			}
		}

		var allSandboxes []*dto.SandboxInfo
		for _, a := range registry.List() {
			sandboxes, err := a.ListSandboxes(r.Context(), opts)
			if err != nil {
				continue
			}
			for _, sb := range sandboxes {
				allSandboxes = append(allSandboxes, sandboxToDTO(sb))
			}
		}

		if allSandboxes == nil {
			allSandboxes = []*dto.SandboxInfo{}
		}
		writeJSON(w, http.StatusOK, allSandboxes)
	}
}

// ListSandboxesHandlerV2 handles GET /v2/sandboxes
func ListSandboxesHandlerV2(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := adapter.ListOptions{}
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil {
				opts.Limit = limit
			}
		}

		var allSandboxes []dto.SandboxInfo
		for _, a := range registry.List() {
			sandboxes, err := a.ListSandboxes(r.Context(), opts)
			if err != nil {
				continue
			}
			for _, sb := range sandboxes {
				allSandboxes = append(allSandboxes, *sandboxToDTO(sb))
			}
		}

		if allSandboxes == nil {
			allSandboxes = []dto.SandboxInfo{}
		}
		writeJSON(w, http.StatusOK, dto.V2SandboxListResponse{Sandboxes: allSandboxes})
	}
}

// GetSandboxHandler handles GET /sandboxes/{sandboxID}
func GetSandboxHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			sandbox, err := a.GetSandbox(r.Context(), sandboxID)
			if err == nil {
				writeJSON(w, http.StatusOK, sandboxToDTO(sandbox))
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// KillSandboxHandler handles DELETE /api/v1/sandboxes/{sandboxID}
func KillSandboxHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			if err := a.KillSandbox(r.Context(), sandboxID); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// PauseSandboxHandler handles POST /api/v1/sandboxes/{sandboxID}/pause
func PauseSandboxHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			if err := a.PauseSandbox(r.Context(), sandboxID); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// ResumeSandboxHandler handles POST /sandboxes/{sandboxID}/resume
func ResumeSandboxHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			sandbox, err := a.ResumeSandbox(r.Context(), sandboxID)
			if err == nil {
				writeJSON(w, http.StatusOK, sandboxToDTO(sandbox))
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// SetTimeoutHandler handles PATCH /api/v1/sandboxes/{sandboxID}/timeout
func SetTimeoutHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req dto.SandboxTimeoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		timeout := time.Duration(req.Timeout) * time.Second

		for _, a := range registry.List() {
			if err := a.SetTimeout(r.Context(), sandboxID, timeout); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Code Execution Handlers ---

// ExecuteCodeHandler handles POST /sandboxes/{sandboxID}/code
func ExecuteCodeHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var dtoReq dto.CodeExecRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req := &adapter.CodeExecutionRequest{
			Code:     dtoReq.Code,
			Language: dtoReq.Language,
			EnvVars:  dtoReq.EnvVars,
		}

		for _, a := range registry.List() {
			result, err := a.ExecuteCode(r.Context(), sandboxID, req)
			if err == nil {
				writeJSON(w, http.StatusOK, &dto.CodeExecResult{
					Stdout:   result.Stdout,
					Stderr:   result.Stderr,
					ExitCode: result.ExitCode,
					Error:    result.Error,
				})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// StartExecutionHandler handles POST /sandboxes/{sandboxID}/code/executions
func StartExecutionHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var dtoReq dto.CodeExecRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req := &adapter.CodeExecutionRequest{
			Code:     dtoReq.Code,
			Language: dtoReq.Language,
			EnvVars:  dtoReq.EnvVars,
		}

		for _, a := range registry.List() {
			result, err := a.ExecuteCode(r.Context(), sandboxID, req)
			if err == nil {
				executionID := "exec-" + sandboxID + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
				writeJSON(w, http.StatusAccepted, map[string]interface{}{
					"executionID": executionID,
					"result": &dto.CodeExecResult{
						Stdout:   result.Stdout,
						Stderr:   result.Stderr,
						ExitCode: result.ExitCode,
						Error:    result.Error,
					},
				})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// GetExecutionHandler handles GET /api/v1/sandboxes/{sandboxID}/code/executions/{executionID}
func GetExecutionHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = chi.URLParam(r, "sandboxID")
		_ = chi.URLParam(r, "executionID")
		// In a full implementation, this would retrieve async execution results
		// For now, return not implemented as executions are synchronous
		writeError(w, http.StatusNotImplemented, "Async executions not yet implemented")
	}
}

// --- Command Handler ---

// RunCommandHandler handles POST /sandboxes/{sandboxID}/commands
func RunCommandHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var dtoReq dto.CommandRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req := &adapter.CommandRequest{
			Command: dtoReq.Command,
			Cwd:     dtoReq.Cwd,
			EnvVars: dtoReq.EnvVars,
		}

		for _, a := range registry.List() {
			result, err := a.RunCommand(r.Context(), sandboxID, req)
			if err == nil {
				writeJSON(w, http.StatusOK, &dto.CommandResult{
					Stdout:   result.Stdout,
					Stderr:   result.Stderr,
					ExitCode: result.ExitCode,
				})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- File Handlers ---

// WriteFileHandler handles POST /sandboxes/{sandboxID}/files
func WriteFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var dtoReq dto.FileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req := &adapter.FileWriteRequest{
			Path:    dtoReq.Path,
			Content: []byte(dtoReq.Content),
		}

		for _, a := range registry.List() {
			if err := a.WriteFile(r.Context(), sandboxID, req); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// ReadFileHandler handles GET /sandboxes/{sandboxID}/files
func ReadFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		path := r.URL.Query().Get("path")
		if path == "" {
			writeError(w, http.StatusBadRequest, "path query parameter is required")
			return
		}

		for _, a := range registry.List() {
			content, err := a.ReadFile(r.Context(), sandboxID, path)
			if err == nil {
				writeJSON(w, http.StatusOK, string(content.Content))
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' or file not found")
	}
}

// UploadFileHandler handles POST /api/v1/sandboxes/{sandboxID}/files/upload
// Supports both multipart/form-data and application/json.
func UploadFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		contentType := r.Header.Get("Content-Type")

		// JSON body: {"path": "...", "content": "..."}
		if strings.Contains(contentType, "application/json") {
			var body struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			req := &adapter.FileWriteRequest{
				Path:    body.Path,
				Content: []byte(body.Content),
			}
			for _, a := range registry.List() {
				if err := a.WriteFile(r.Context(), sandboxID, req); err == nil {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
			return
		}

		// Multipart form upload
		if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
			writeError(w, http.StatusBadRequest, "Failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "file field is required")
			return
		}
		defer func() { _ = file.Close() }()

		path := r.FormValue("path")
		if path == "" {
			path = header.Filename
		}

		req := &adapter.FileUploadRequest{
			Path:   path,
			Reader: file,
		}

		for _, a := range registry.List() {
			if err := a.UploadFile(r.Context(), sandboxID, req); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// DownloadFileHandler handles GET /api/v1/sandboxes/{sandboxID}/files/download
func DownloadFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		path := r.URL.Query().Get("path")
		if path == "" {
			writeError(w, http.StatusBadRequest, "path query parameter is required")
			return
		}

		for _, a := range registry.List() {
			reader, err := a.DownloadFile(r.Context(), sandboxID, path)
			if err == nil {
				defer func() { _ = reader.Close() }()
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", "attachment; filename="+path)
				if _, err := io.Copy(w, reader); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
				}
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' or file not found")
	}
}

// ListFilesHandler handles POST /sandboxes/{sandboxID}/files/list
func ListFilesHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		path := "/"

		// Try to read path from body (E2B format) or query param
		if r.Body != nil && r.ContentLength > 0 {
			var bodyReq dto.FileListRequest
			if err := json.NewDecoder(r.Body).Decode(&bodyReq); err == nil && bodyReq.Path != "" {
				path = bodyReq.Path
			}
		}
		if qPath := r.URL.Query().Get("path"); qPath != "" {
			path = qPath
		}

		for _, a := range registry.List() {
			files, err := a.ListFiles(r.Context(), sandboxID, path)
			if err == nil {
				writeJSON(w, http.StatusOK, dto.FileListResponse{
					Entries: adapterFileInfosToDTO(files),
				})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// MakeDirHandler handles POST /sandboxes/{sandboxID}/files/make-dir
func MakeDirHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req dto.MakeDirRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.MakeDir(r.Context(), sandboxID, req.Path); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// RemoveFileHandler handles POST /sandboxes/{sandboxID}/files/remove
func RemoveFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req dto.FileRemoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.RemoveFile(r.Context(), sandboxID, req.Path); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Template Handlers ---

// ListTemplatesHandler handles GET /templates
func ListTemplatesHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var allTemplates []*dto.TemplateInfo
		for _, a := range registry.List() {
			templates, err := a.ListTemplates(r.Context(), adapter.ListOptions{})
			if err != nil {
				continue
			}
			for _, t := range templates {
				allTemplates = append(allTemplates, templateToDTO(t))
			}
		}
		if allTemplates == nil {
			allTemplates = []*dto.TemplateInfo{}
		}
		writeJSON(w, http.StatusOK, allTemplates)
	}
}

// ListTemplatesHandlerV2 handles GET /v2/templates
func ListTemplatesHandlerV2(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var allTemplates []dto.V2TemplateInfo
		for _, a := range registry.List() {
			templates, err := a.ListTemplates(r.Context(), adapter.ListOptions{})
			if err != nil {
				continue
			}
			for _, t := range templates {
				allTemplates = append(allTemplates, dto.V2TemplateInfo{
					TemplateID: t.TemplateID,
					BuildID:    t.BuildID,
					CPUCount:   t.CPUCount,
					MemoryMB:   t.MemoryMB,
					Public:     t.Public,
					Ready:      true,
					CreatedAt:  t.CreatedAt,
				})
			}
		}
		if allTemplates == nil {
			allTemplates = []dto.V2TemplateInfo{}
		}
		writeJSON(w, http.StatusOK, allTemplates)
	}
}

// GetTemplateHandler handles GET /templates/{templateID}
func GetTemplateHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		for _, a := range registry.List() {
			tmpl, err := a.GetTemplate(r.Context(), templateID)
			if err == nil {
				writeJSON(w, http.StatusOK, templateToDTO(tmpl))
				return
			}
		}

		writeError(w, http.StatusNotFound, "Template '"+templateID+"' not found")
	}
}

// CreateTemplateHandler handles POST /templates
func CreateTemplateHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dtoReq dto.TemplateBuildRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		req := &adapter.CreateTemplateRequest{
			Name:       dtoReq.Name,
			Dockerfile: dtoReq.Dockerfile,
			StartCmd:   dtoReq.StartCmd,
			CPUCount:   dtoReq.CPUCount,
			MemoryMB:   dtoReq.MemoryMB,
		}

		for _, a := range registry.List() {
			build, err := a.CreateTemplate(r.Context(), req)
			if err == nil {
				writeJSON(w, http.StatusAccepted, &dto.TemplateBuildResponse{
					TemplateID: build.TemplateID,
					BuildID:    build.BuildID,
					Status:     build.Status,
				})
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports template creation")
	}
}

// CreateTemplateHandlerV2 handles POST /v2/templates
func CreateTemplateHandlerV2(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dtoReq dto.V2TemplateCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&dtoReq); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		req := &adapter.CreateTemplateRequest{
			Name:       dtoReq.Name,
			Dockerfile: dtoReq.Dockerfile,
			StartCmd:   dtoReq.StartCmd,
			CPUCount:   dtoReq.CPUCount,
			MemoryMB:   dtoReq.MemoryMB,
		}

		for _, a := range registry.List() {
			build, err := a.CreateTemplate(r.Context(), req)
			if err == nil {
				writeJSON(w, http.StatusAccepted, &dto.V2TemplateInfo{
					TemplateID: build.TemplateID,
					BuildID:    build.BuildID,
					CPUCount:   dtoReq.CPUCount,
					MemoryMB:   dtoReq.MemoryMB,
					Ready:      false,
				})
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports template creation")
	}
}

// DeleteTemplateHandler handles DELETE /api/v1/templates/{templateID}
func DeleteTemplateHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		for _, a := range registry.List() {
			if err := a.DeleteTemplate(r.Context(), templateID); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Template '"+templateID+"' not found")
	}
}

// --- Warm Pool Handlers ---

// ListWarmPoolsHandler handles GET /api/v1/warm-pools
func ListWarmPoolsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var allPools []*adapter.WarmPool
		for _, a := range registry.List() {
			pools, err := a.ListWarmPools(r.Context())
			if err != nil {
				continue
			}
			allPools = append(allPools, pools...)
		}
		if allPools == nil {
			allPools = []*adapter.WarmPool{}
		}
		writeJSON(w, http.StatusOK, allPools)
	}
}

// CreateWarmPoolHandler handles POST /api/v1/warm-pools
func CreateWarmPoolHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adapter.WarmPoolCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		for _, a := range registry.List() {
			pool, err := a.CreateWarmPool(r.Context(), &req)
			if err == nil {
				writeJSON(w, http.StatusCreated, pool)
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports warm pools")
	}
}

// GetWarmPoolHandler handles GET /api/v1/warm-pools/{warmPoolID}
func GetWarmPoolHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		warmPoolID := chi.URLParam(r, "warmPoolID")

		for _, a := range registry.List() {
			pool, err := a.GetWarmPool(r.Context(), warmPoolID)
			if err == nil {
				writeJSON(w, http.StatusOK, pool)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Warm pool '"+warmPoolID+"' not found")
	}
}

// DeleteWarmPoolHandler handles DELETE /api/v1/warm-pools/{warmPoolID}
func DeleteWarmPoolHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		warmPoolID := chi.URLParam(r, "warmPoolID")

		for _, a := range registry.List() {
			if err := a.DeleteWarmPool(r.Context(), warmPoolID); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Warm pool '"+warmPoolID+"' not found")
	}
}

// UpdateWarmPoolSizeHandler handles POST /api/v1/warm-pools/{warmPoolID}/size
func UpdateWarmPoolSizeHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		warmPoolID := chi.URLParam(r, "warmPoolID")

		var req struct {
			TargetSize int `json:"targetSize"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.UpdateWarmPoolSize(r.Context(), warmPoolID, req.TargetSize); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Warm pool '"+warmPoolID+"' not found")
	}
}

// --- Process Handlers ---

// ListProcessesHandler handles GET /api/v1/sandboxes/{sandboxID}/processes
func ListProcessesHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			procs, err := a.ListProcesses(r.Context(), sandboxID)
			if err == nil {
				if procs == nil {
					procs = []*adapter.ProcessInfo{}
				}
				writeJSON(w, http.StatusOK, procs)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// KillProcessHandler handles POST /api/v1/sandboxes/{sandboxID}/processes/{processID}/kill
func KillProcessHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		processID := chi.URLParam(r, "processID")

		for _, a := range registry.List() {
			if err := a.KillProcess(r.Context(), sandboxID, processID); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' or process '"+processID+"' not found")
	}
}

// SendStdinHandler handles POST /api/v1/sandboxes/{sandboxID}/processes/{processID}/stdin
func SendStdinHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		processID := chi.URLParam(r, "processID")

		var req struct {
			Data string `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.SendStdin(r.Context(), sandboxID, processID, req.Data); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' or process '"+processID+"' not found")
	}
}

// --- Snapshot Handlers ---

// CreateSnapshotHandler handles POST /api/v1/sandboxes/{sandboxID}/snapshots
func CreateSnapshotHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req adapter.SnapshotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		for _, a := range registry.List() {
			snap, err := a.CreateSnapshot(r.Context(), sandboxID, &req)
			if err == nil {
				writeJSON(w, http.StatusCreated, snap)
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports snapshots")
	}
}

// ListSnapshotsHandler handles GET /api/v1/sandboxes/{sandboxID}/snapshots
func ListSnapshotsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			snaps, err := a.ListSnapshots(r.Context(), sandboxID)
			if err == nil {
				if snaps == nil {
					snaps = []*adapter.Snapshot{}
				}
				writeJSON(w, http.StatusOK, snaps)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Port Handlers ---

// ListPortsHandler handles GET /api/v1/sandboxes/{sandboxID}/ports
func ListPortsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			ports, err := a.ListPorts(r.Context(), sandboxID)
			if err == nil {
				if ports == nil {
					ports = []*adapter.PortInfo{}
				}
				writeJSON(w, http.StatusOK, map[string]interface{}{"ports": ports})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// GetPortURLHandler handles GET /api/v1/sandboxes/{sandboxID}/ports/{port}
func GetPortURLHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")
		portStr := chi.URLParam(r, "port")
		port, err := strconv.Atoi(portStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid port number")
			return
		}

		for _, a := range registry.List() {
			url, err := a.GetPortURL(r.Context(), sandboxID, port)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]string{"url": url})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' or port not found")
	}
}

// --- Access Token Handler ---

// GetAccessTokenHandler handles POST /api/v1/sandboxes/{sandboxID}/access-token
func GetAccessTokenHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			token, err := a.GetAccessToken(r.Context(), sandboxID)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"accessToken": token.Token,
					"expiresAt":   token.ExpiresAt,
				})
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports access tokens")
	}
}

// --- Template Build & Alias Handlers ---

// TriggerBuildHandler handles POST /api/v1/templates/{templateID}/builds
func TriggerBuildHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		var req adapter.BuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}

		for _, a := range registry.List() {
			build, err := a.TriggerBuild(r.Context(), templateID, &req)
			if err == nil {
				writeJSON(w, http.StatusAccepted, build)
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports template builds")
	}
}

// GetBuildStatusHandler handles POST /api/v1/templates/{templateID}/builds/{buildID}/status
func GetBuildStatusHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")
		buildID := chi.URLParam(r, "buildID")

		for _, a := range registry.List() {
			status, err := a.GetBuildStatus(r.Context(), templateID, buildID)
			if err == nil {
				writeJSON(w, http.StatusOK, status)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Build '"+buildID+"' not found")
	}
}

// CreateAliasHandler handles POST /api/v1/templates/{templateID}/aliases
func CreateAliasHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		var req struct {
			Alias string `json:"alias"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.CreateAlias(r.Context(), templateID, req.Alias); err == nil {
				w.WriteHeader(http.StatusCreated)
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports aliases")
	}
}

// DeleteAliasHandler handles DELETE /api/v1/templates/{templateID}/aliases/{alias}
func DeleteAliasHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")
		alias := chi.URLParam(r, "alias")

		for _, a := range registry.List() {
			if err := a.DeleteAlias(r.Context(), templateID, alias); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Alias '"+alias+"' not found for template '"+templateID+"'")
	}
}

// --- Health Handlers ---

// HealthHandler handles GET /healthz
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ReadyHandler handles GET /readyz
func ReadyHandler(registry *adapter.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results := registry.HealthCheckAll(r.Context())
		allHealthy := true
		backendStatus := make(map[string]string)

		for name, err := range results {
			if err != nil {
				allHealthy = false
				backendStatus[name] = "unhealthy: " + err.Error()
			} else {
				backendStatus[name] = "healthy"
			}
		}

		resp := map[string]interface{}{
			"status":   "ok",
			"backends": backendStatus,
		}

		status := http.StatusOK
		if !allHealthy {
			resp["status"] = "degraded"
			status = http.StatusServiceUnavailable
		}

		writeJSON(w, status, resp)
	}
}

// --- Environment Variables Handler ---

// SetEnvsHandler handles POST /sandboxes/{sandboxID}/envs
func SetEnvsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req dto.SetEnvsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.SetEnvs(r.Context(), sandboxID, req.Envs); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Logs Handler ---

// GetLogsHandler handles GET /sandboxes/{sandboxID}/logs and GET /v2/sandboxes/{sandboxID}/logs
func GetLogsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		for _, a := range registry.List() {
			logs, err := a.GetLogs(r.Context(), sandboxID)
			if err == nil {
				dtoLogs := make([]dto.LogEntry, len(logs))
				for i, l := range logs {
					dtoLogs[i] = dto.LogEntry{
						Timestamp: l.Timestamp,
						Message:   l.Message,
						Level:     l.Level,
						Source:    l.Source,
					}
				}
				writeJSON(w, http.StatusOK, dto.SandboxLogsResponse{Logs: dtoLogs})
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Metrics Handler ---

// GetMetricsHandler handles GET /v2/sandboxes/{sandboxID}/metrics
func GetMetricsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		// Metrics are not yet implemented at adapter level; return empty metrics
		// This endpoint exists for E2B API compatibility
		_ = sandboxID
		writeJSON(w, http.StatusOK, dto.SandboxMetrics{
			Timestamp: time.Now(),
		})
	}
}

// --- File Move Handler ---

// MoveFileHandler handles POST /sandboxes/{sandboxID}/filesystem/move
func MoveFileHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sandboxID := chi.URLParam(r, "sandboxID")

		var req dto.MoveFileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			if err := a.MoveFile(r.Context(), sandboxID, req.Source, req.Destination); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Sandbox '"+sandboxID+"' not found")
	}
}

// --- Update Template Handler ---

// UpdateTemplateHandler handles PATCH /v2/templates/{templateID}
func UpdateTemplateHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		var req dto.UpdateTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		// Get existing template, update fields, and re-create
		for _, a := range registry.List() {
			tmpl, err := a.GetTemplate(r.Context(), templateID)
			if err == nil {
				// Template exists - update is a no-op for most fields in backends
				// Return updated template info
				info := templateToDTO(tmpl)
				if req.Name != "" {
					info.TemplateID = tmpl.TemplateID
				}
				if req.Public != nil {
					info.Public = *req.Public
				}
				if req.CPUCount != nil {
					info.CPUCount = *req.CPUCount
				}
				if req.MemoryMB != nil {
					info.MemoryMB = *req.MemoryMB
				}
				writeJSON(w, http.StatusOK, info)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Template '"+templateID+"' not found")
	}
}

// --- Template Tags Handlers ---

// CreateTagHandler handles POST /templates/{templateID}/tags
func CreateTagHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		var req dto.CreateTagRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, a := range registry.List() {
			tag, err := a.CreateTag(r.Context(), templateID, &adapter.TagRequest{
				Name:    req.Name,
				BuildID: req.BuildID,
			})
			if err == nil {
				writeJSON(w, http.StatusCreated, dto.TagInfo{
					Name:       tag.Name,
					TemplateID: templateID,
					BuildID:    tag.BuildID,
					CreatedAt:  tag.CreatedAt,
				})
				return
			}
		}

		writeError(w, http.StatusServiceUnavailable, "No backend supports template tags")
	}
}

// ListTagsHandler handles GET /templates/{templateID}/tags
func ListTagsHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")

		for _, a := range registry.List() {
			tags, err := a.ListTags(r.Context(), templateID)
			if err == nil {
				dtoTags := make([]dto.TagInfo, len(tags))
				for i, t := range tags {
					dtoTags[i] = dto.TagInfo{
						Name:       t.Name,
						TemplateID: templateID,
						BuildID:    t.BuildID,
						CreatedAt:  t.CreatedAt,
					}
				}
				writeJSON(w, http.StatusOK, dtoTags)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Template '"+templateID+"' not found")
	}
}

// DeleteTagHandler handles DELETE /templates/{templateID}/tags/{tagName}
func DeleteTagHandler(registry *adapter.Registry, router *routing.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templateID := chi.URLParam(r, "templateID")
		tagName := chi.URLParam(r, "tagName")

		for _, a := range registry.List() {
			if err := a.DeleteTag(r.Context(), templateID, tagName); err == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		writeError(w, http.StatusNotFound, "Tag '"+tagName+"' not found for template '"+templateID+"'")
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes an E2B-compatible error response: {"code": int, "message": string}
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    status,
		"message": message,
	})
}

// sandboxToDTO converts an adapter.Sandbox to a dto.SandboxInfo (E2B wire format).
func sandboxToDTO(sb *adapter.Sandbox) *dto.SandboxInfo {
	state := "running"
	switch sb.Status {
	case adapter.SandboxStatusRunning:
		state = "running"
	case adapter.SandboxStatusPaused:
		state = "paused"
	case adapter.SandboxStatusStopped:
		state = "stopped"
	case adapter.SandboxStatusError:
		state = "error"
	case adapter.SandboxStatusStarting:
		state = "starting"
	}
	return &dto.SandboxInfo{
		SandboxID:  sb.SandboxID,
		TemplateID: sb.TemplateID,
		Alias:      sb.Alias,
		ClientID:   sb.ClientID,
		StartedAt:  sb.StartedAt,
		EndAt:      sb.EndAt,
		State:      state,
		Metadata:   sb.Metadata,
	}
}

// templateToDTO converts an adapter.Template to a dto.TemplateInfo (E2B wire format).
func templateToDTO(tmpl *adapter.Template) *dto.TemplateInfo {
	return &dto.TemplateInfo{
		TemplateID: tmpl.TemplateID,
		BuildID:    tmpl.BuildID,
		CPUCount:   tmpl.CPUCount,
		MemoryMB:   tmpl.MemoryMB,
		Public:     tmpl.Public,
		CreatedAt:  tmpl.CreatedAt,
	}
}

// generateEnvdToken generates a mock envd access token for sandbox connections.
func generateEnvdToken(sandboxID string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("envd_%s_%s", sandboxID, hex.EncodeToString(b))
}

// adapterFileInfosToDTO converts adapter.FileInfo slice to DTO FileEntry slice.
func adapterFileInfosToDTO(infos []adapter.FileInfo) []dto.FileEntry {
	entries := make([]dto.FileEntry, len(infos))
	for i, f := range infos {
		fileType := "file"
		if f.IsDir {
			fileType = "dir"
		}
		entries[i] = dto.FileEntry{
			Name:         f.Name,
			Type:         fileType,
			Size:         f.Size,
			LastModified: f.ModTime,
		}
	}
	return entries
}
