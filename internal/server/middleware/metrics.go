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

package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/e2bgateway/e2bgateway/internal/metrics"
)

// Metrics records request counts and latency into the gateway's Prometheus
// registry. It is a no-op when metrics are disabled. The configured scrape
// path (observability.metrics.path; defaults to "/metrics" when empty) is
// not recorded to avoid self-noise.
//
// Register Metrics OUTSIDE (before) Recovery so that recovered panics are
// observed: Recovery converts the panic into a 500 response below Metrics,
// which then records it.
func Metrics(scrapePath string) func(http.Handler) http.Handler {
	if scrapePath == "" {
		scrapePath = metricsPath
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == scrapePath || metrics.Current() == nil {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)

			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil {
				if p := rc.RoutePattern(); p != "" {
					route = p
				}
			}
			metrics.ObserveHTTP(route, r.Method, ww.status, time.Since(start))
		})
	}
}
