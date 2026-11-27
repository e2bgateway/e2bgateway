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

package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

// CodeExecRequest holds code and runtime settings for a synchronous execution.
type CodeExecRequest = dto.CodeExecRequest

// CodeExecResult holds the output and outcome of a synchronous execution.
type CodeExecResult = dto.CodeExecResult

// CommandRequest describes a command to run in a sandbox.
type CommandRequest = dto.CommandRequest

// CommandResult holds the output and exit status of a command.
type CommandResult = dto.CommandResult

// FileEntry describes a directory entry in a sandbox.
type FileEntry = dto.FileEntry

// TemplateBuildRequest describes a new sandbox template.
type TemplateBuildRequest = dto.TemplateBuildRequest

// TemplateBuildResponse identifies a template and its build.
type TemplateBuildResponse = dto.TemplateBuildResponse

// BuildStatusResponse describes the current state of a template build.
type BuildStatusResponse = dto.BuildStatusResponse

// TagInfo describes a template tag.
type TagInfo = dto.TagInfo

func (c *Client) requestJSON(ctx context.Context, method, path string, status int, body, result any) error {
	resp, err := c.doRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer drain(resp)
	if err := c.checkStatus(resp, status); err != nil {
		return err
	}
	return c.decodeResponse(resp, result)
}

// ExecuteCode executes code synchronously and returns its result.
func (c *Client) ExecuteCode(ctx context.Context, id string, req CodeExecRequest) (CodeExecResult, error) {
	var result CodeExecResult
	err := c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/code", http.StatusOK, req, &result)
	return result, err
}

// RunCommand runs a command in the given sandbox and returns its output and exit status.
func (c *Client) RunCommand(ctx context.Context, id string, req CommandRequest) (CommandResult, error) {
	var result CommandResult
	err := c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/commands", http.StatusOK, req, &result)
	return result, err
}

// WriteFile writes UTF-8 text. It is not a binary upload API.
func (c *Client) WriteFile(ctx context.Context, id, path, content string) error {
	return c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/files", http.StatusNoContent,
		dto.FileWriteRequest{Path: path, Content: content}, nil)
}

// ReadFile reads a UTF-8 text file from the sandbox.
func (c *Client) ReadFile(ctx context.Context, id, path string) (string, error) {
	var text string
	query := url.Values{"path": {path}}
	err := c.requestJSON(ctx, http.MethodGet, "/sandboxes/"+urlPathEscape(id)+"/files?"+query.Encode(), http.StatusOK, nil, &text)
	return text, err
}

// ListFiles returns the entries at path in the sandbox.
func (c *Client) ListFiles(ctx context.Context, id, path string) ([]FileEntry, error) {
	var result dto.FileListResponse
	err := c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/filesystem/list", http.StatusOK,
		dto.FileListRequest{Path: path}, &result)
	return result.Entries, err
}

// MakeDir creates a directory in the sandbox.
func (c *Client) MakeDir(ctx context.Context, id, path string) error {
	return c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/filesystem/mkdir", http.StatusNoContent,
		dto.MakeDirRequest{Path: path}, nil)
}

// RemoveFile removes the specified sandbox file.
func (c *Client) RemoveFile(ctx context.Context, id, path string) error {
	return c.requestJSON(ctx, http.MethodPost, "/sandboxes/"+urlPathEscape(id)+"/filesystem/rm", http.StatusNoContent,
		dto.FileRemoveRequest{Path: path}, nil)
}

// GetPortURL returns the backend URL for a sandbox port.
func (c *Client) GetPortURL(ctx context.Context, id string, port int) (string, error) {
	var result struct {
		URL string `json:"url"`
	}
	err := c.requestJSON(ctx, http.MethodGet, "/sandboxes/"+urlPathEscape(id)+"/ports/"+strconv.Itoa(port), http.StatusOK, nil, &result)
	return result.URL, err
}

// CreateTemplate registers a template and returns its build reference.
func (c *Client) CreateTemplate(ctx context.Context, req TemplateBuildRequest) (TemplateBuildResponse, error) {
	var result TemplateBuildResponse
	err := c.requestJSON(ctx, http.MethodPost, "/templates", http.StatusAccepted, req, &result)
	return result, err
}

// GetBuildStatus retrieves a template build status (POST on the E2B API).
func (c *Client) GetBuildStatus(ctx context.Context, templateID, buildID string) (BuildStatusResponse, error) {
	var result BuildStatusResponse
	path := "/templates/" + urlPathEscape(templateID) + "/builds/" + urlPathEscape(buildID) + "/status"
	err := c.requestJSON(ctx, http.MethodPost, path, http.StatusOK, nil, &result)
	return result, err
}

// CreateTemplateAlias adds an alias for a template.
func (c *Client) CreateTemplateAlias(ctx context.Context, id, alias string) error {
	return c.requestJSON(ctx, http.MethodPost, "/templates/"+urlPathEscape(id)+"/aliases", http.StatusCreated,
		map[string]string{"alias": alias}, nil)
}

// CreateTemplateTag attaches a tag to a template build.
func (c *Client) CreateTemplateTag(ctx context.Context, id, name, buildID string) (TagInfo, error) {
	var result TagInfo
	err := c.requestJSON(ctx, http.MethodPost, "/templates/"+urlPathEscape(id)+"/tags", http.StatusCreated,
		dto.CreateTagRequest{Name: name, BuildID: buildID}, &result)
	return result, err
}

// ListTemplateTags lists the tags of a template.
func (c *Client) ListTemplateTags(ctx context.Context, id string) ([]TagInfo, error) {
	var tags []TagInfo
	err := c.requestJSON(ctx, http.MethodGet, "/templates/"+urlPathEscape(id)+"/tags", http.StatusOK, nil, &tags)
	return tags, err
}
