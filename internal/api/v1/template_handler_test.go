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

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/api/dto"
)

type templateLookupStub struct {
	adapter.SandboxAdapter
	template *adapter.Template
}

func (s templateLookupStub) GetTemplate(_ context.Context, id string) (*adapter.Template, error) {
	if s.template == nil || s.template.TemplateID != id {
		return nil, errors.New("template not found")
	}
	return s.template, nil
}

func TestGetTemplateHandler_PrefersManagedTemplate(t *testing.T) {
	const templateID = "base"
	managed := &adapter.Template{
		TemplateID: templateID,
		BuildID:    "build-base",
		CPUCount:   2,
		MemoryMB:   1024,
		Public:     true,
		CreatedAt:  time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
	}
	synthetic := &adapter.Template{
		TemplateID: templateID,
		CreatedAt:  managed.CreatedAt.Add(time.Hour),
		Synthetic:  true,
	}
	owner := templateLookupStub{template: managed}
	fallback := templateLookupStub{template: synthetic}
	miss := templateLookupStub{}

	tests := []struct {
		name     string
		backends []adapter.SandboxAdapter
		want     *adapter.Template
	}{
		{name: "synthetic before managed", backends: []adapter.SandboxAdapter{fallback, owner}, want: managed},
		{name: "managed before synthetic", backends: []adapter.SandboxAdapter{owner, fallback}, want: managed},
		{name: "synthetic without owner", backends: []adapter.SandboxAdapter{fallback, miss}, want: synthetic},
		{name: "no match", backends: []adapter.SandboxAdapter{miss}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := chi.NewRouter()
			r.Get("/templates/{templateID}", getTemplateHandler(func() []adapter.SandboxAdapter {
				return tt.backends
			}))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/templates/"+templateID, nil))

			if tt.want == nil {
				require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
				return
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var got dto.TemplateInfo
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			require.Equal(t, dto.TemplateInfo{
				TemplateID: tt.want.TemplateID,
				BuildID:    tt.want.BuildID,
				CPUCount:   tt.want.CPUCount,
				MemoryMB:   tt.want.MemoryMB,
				Public:     tt.want.Public,
				CreatedAt:  tt.want.CreatedAt,
			}, got)
			require.NotContains(t, w.Body.String(), `"synthetic"`)
		})
	}
}
