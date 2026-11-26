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
	"net/http"
	"runtime"
)

// BuildInfo carries ldflags-injected build metadata exposed by GET /versionz.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// VersionHandler handles GET /versionz. Like /healthz it requires no auth.
// Empty fields fall back to dev/unknown so plain `go build` stays useful.
func VersionHandler(info BuildInfo) http.HandlerFunc {
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.BuildDate == "" {
		info.BuildDate = "unknown"
	}
	resp := map[string]string{
		"version":   info.Version,
		"commit":    info.Commit,
		"buildDate": info.BuildDate,
		"goVersion": runtime.Version(),
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, resp)
	}
}