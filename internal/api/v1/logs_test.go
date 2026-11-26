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

package v1_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/e2bcloud"
	mockadapter "github.com/e2bgateway/e2bgateway/internal/adapter/mock"
	"github.com/e2bgateway/e2bgateway/internal/adapter/opensandbox"
	v1 "github.com/e2bgateway/e2bgateway/internal/api/v1"
	"github.com/e2bgateway/e2bgateway/internal/sessionstore"
)

type logBackend struct {
	adapter.SandboxAdapter
	name string
	read func() ([]*adapter.LogEntry, error)
}

func (b logBackend) Name() string                                                 { return b.name }
func (b logBackend) GetLogs(context.Context, string) ([]*adapter.LogEntry, error) { return b.read() }

type logLookupFailure struct{ sessionstore.Store }

func (logLookupFailure) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("sensitive store payload")
}

func requestLogs(reg *adapter.Registry, id, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := testWithChiParam(http.MethodGet, "/sandboxes/"+id+"/logs"+query, "", map[string]string{"sandboxID": id})
	v1.GetLogsHandler(reg)(w, r)
	return w
}

type filteredLogBackend struct {
	logBackend
	readFiltered func(string, adapter.LogOptions) ([]*adapter.LogEntry, error)
}

func (b filteredLogBackend) GetLogsWithOptions(_ context.Context, id string, opts adapter.LogOptions) ([]*adapter.LogEntry, error) {
	return b.readFiltered(id, opts)
}

func TestGetLogsForwardsFilters(t *testing.T) {
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(filteredLogBackend{
		logBackend: logBackend{name: "filtered", read: func() ([]*adapter.LogEntry, error) {
			t.Fatal("called unfiltered backend method")
			return nil, nil
		}},
		readFiltered: func(id string, opts adapter.LogOptions) ([]*adapter.LogEntry, error) {
			assert.Equal(t, "sb", id)
			assert.EqualValues(t, 1000, opts.TailLines)
			require.NotNil(t, opts.SinceTime)
			assert.Equal(t, "2026-11-19T12:34:56.123456789Z", opts.SinceTime.UTC().Format(time.RFC3339Nano))
			return nil, nil
		},
	}))
	w := requestLogs(reg, "sb", "?tailLines=1000&sinceTime=2026-11-19T13:34:56.123456789123%2B01:00")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"logs":[]}`, w.Body.String())
}

func TestGetLogsRejectsInvalidSandboxPath(t *testing.T) {
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(logBackend{name: "backend", read: func() ([]*adapter.LogEntry, error) {
		t.Error("invalid ID reached backend")
		return nil, nil
	}}))
	for _, id := range []string{"..", "sb%2Fother", "sb/other", "sb?query=other"} {
		w := requestLogs(reg, id, "")
		assert.Equal(t, http.StatusNotFound, w.Code, "ID %q", id)
	}
}

func TestGetLogsRouting(t *testing.T) {
	for _, tt := range []struct {
		name, mapping string
		other, broken bool
		want, calls   int
	}{
		{"mapped upstream failure", "owner", true, false, 502, 1},
		{"mapped empty success", "other", true, false, 200, 0},
		{"unmapped multiple", "", true, false, 404, 0},
		{"mapped unknown even with sole backend", "gone", false, false, 404, 0},
		{"store failure even with sole backend", "", false, true, 502, 0},
		{"unmapped sole", "", false, false, 502, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := sessionstore.NewMemory()
			if tt.broken {
				store = logLookupFailure{store}
			}
			reg := adapter.NewRegistry(store, time.Hour)
			var ownerCalls, otherCalls int
			require.NoError(t, reg.Register(logBackend{name: "owner", read: func() ([]*adapter.LogEntry, error) {
				ownerCalls++
				return nil, errors.New("not found: sensitive backend payload")
			}}))
			if tt.other {
				require.NoError(t, reg.Register(logBackend{name: "other", read: func() ([]*adapter.LogEntry, error) {
					otherCalls++
					return nil, nil
				}}))
			}
			if tt.mapping != "" {
				require.NoError(t, reg.SandboxBackend().Set(t.Context(), "sb", tt.mapping))
			}
			w := requestLogs(reg, "sb", "")
			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.Equal(t, tt.calls, ownerCalls)
			if tt.want == http.StatusOK {
				assert.Equal(t, 1, otherCalls)
				assert.JSONEq(t, `{"logs":[]}`, w.Body.String())
			} else {
				assert.Zero(t, otherCalls)
				assert.NotContains(t, w.Body.String(), "sensitive")
			}
		})
	}
}

func TestGetLogsUnsupportedOperation(t *testing.T) {
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	require.NoError(t, reg.Register(logBackend{name: "substrate", read: func() ([]*adapter.LogEntry, error) {
		return nil, fmt.Errorf("logs is not supported by the agent-substrate backend: %w", adapter.ErrUnsupported)
	}}))
	w := requestLogs(reg, "sb", "")
	assert.Equal(t, http.StatusNotImplemented, w.Code, w.Body.String())
}

func TestGetLogsRejectsInvalidFilters(t *testing.T) {
	for _, query := range []string{
		"?tailLines=", "?tailLines=0", "?tailLines=-1", "?tailLines=1001", "?tailLines=1.5",
		"?tailLines=1&tailLines=2", "?tailLines=%zz", "?sinceTime=", "?sinceTime=yesterday",
		"?sinceTime=2026-11-19T12:34:56%2B24:00",
		"?sinceTime=2026-11-19T12:34:56Z&sinceTime=2026-11-19T12:34:56Z",
	} {
		t.Run(query, func(t *testing.T) {
			reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
			require.NoError(t, reg.Register(filteredLogBackend{
				logBackend: logBackend{name: "backend"},
				readFiltered: func(string, adapter.LogOptions) ([]*adapter.LogEntry, error) {
					t.Error("invalid query reached backend")
					return nil, nil
				},
			}))
			w := requestLogs(reg, "sb", query)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func TestGetLogsMock(t *testing.T) {
	reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
	a := mockadapter.New(reg)
	require.NoError(t, reg.Register(a))
	sb, err := a.CreateSandbox(t.Context(), &adapter.CreateSandboxRequest{TemplateID: "base"})
	require.NoError(t, err)
	w := requestLogs(reg, sb.SandboxID, "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "sandbox started")
	assert.Equal(t, http.StatusBadRequest, requestLogs(reg, sb.SandboxID, "?tailLines=1000").Code)
	assert.Equal(t, http.StatusNotFound, requestLogs(reg, "missing", "").Code)
}

func TestGetLogsE2BCloud(t *testing.T) {
	for _, tt := range []struct{ upstream, want int }{{200, 200}, {404, 404}, {503, 502}} {
		t.Run(http.StatusText(tt.upstream), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/sandboxes/sb/logs", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.upstream)
				if tt.upstream == 200 {
					_, _ = io.WriteString(w, `{"logs":[{"timestamp":"2026-11-19T12:34:56Z","message":"cloud log","level":"info","source":"sandbox"}]}`)
				} else {
					_, _ = io.WriteString(w, `{"message":"sensitive backend payload"}`)
				}
			}))
			t.Cleanup(s.Close)
			reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
			require.NoError(t, reg.Register(e2bcloud.NewAdapterWithClient("cloud", e2bcloud.NewClient(e2bcloud.ClientConfig{Endpoint: s.URL}), reg)))
			w := requestLogs(reg, "sb", "")
			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "sensitive")
			if tt.want == 200 {
				assert.JSONEq(t, `{"logs":[{"timestamp":"2026-11-19T12:34:56Z","message":"cloud log","level":"info","source":"sandbox"}]}`, w.Body.String())
			}
		})
	}
}

func TestGetLogsOpenSandbox(t *testing.T) {
	for _, tt := range []struct {
		name, query    string
		upstream, want int
		mapped         bool
	}{
		{"present", "", 200, 501, false},
		{"present with filters", "?tailLines=1", 200, 501, false},
		{"missing", "", 404, 404, false},
		{"outage", "", 403, 502, false},
		{"mapped missing with competing backend", "", 404, 404, true},
		{"mapped unsupported with competing backend", "", 200, 501, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/sandboxes/sb", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.upstream)
				if tt.upstream == 200 {
					_, _ = io.WriteString(w, `{"id":"sb"}`)
				} else {
					_, _ = io.WriteString(w, `{"message":"sensitive backend payload"}`)
				}
			}))
			t.Cleanup(s.Close)
			reg := adapter.NewRegistry(sessionstore.NewMemory(), time.Hour)
			a, err := opensandbox.New(opensandbox.AdapterConfig{Name: "open", BaseURL: s.URL})
			require.NoError(t, err)
			require.NoError(t, reg.Register(a))
			if tt.mapped {
				require.NoError(t, reg.SandboxBackend().Set(t.Context(), "sb", "open"))
				require.NoError(t, reg.Register(logBackend{name: "other", read: func() ([]*adapter.LogEntry, error) {
					t.Error("another backend tried to mask the mapped owner result")
					return nil, nil
				}}))
			}
			w := requestLogs(reg, "sb", tt.query)
			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "sensitive")
		})
	}
}
