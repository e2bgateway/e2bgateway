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

// sandboxCacheKey namespaces cached sandbox metadata by backend so the same
// sandbox ID on two backends never collides.
func sandboxCacheKey(backend, sandboxID string) string {
	return "sbx:" + backend + ":" + sandboxID
}

// templateCacheKey is the per-backend template list cache key.
func templateCacheKey(backend string) string {
	return "tmpl:" + backend
}
