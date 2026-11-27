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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

func TestRecordCache(t *testing.T) {
	Init(config.MetricsConfig{Enabled: true}, Sources{})
	t.Cleanup(Reset)

	RecordCache("sandbox_metadata", "l1_hit")
	RecordCache("sandbox_metadata", "l1_hit")
	RecordCache("sandbox_metadata", "l2_hit")
	RecordCache("template_list", "miss")
	RecordCache("template_list", "invalidation")
	RecordCache("template_list", "l2_error")
	RecordCache("template_list", "unknown_event") // must not panic

	m := Current()
	assert.Equal(t, 2.0, testutil.ToFloat64(m.cacheL1Hits.WithLabelValues("sandbox_metadata")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheL2Hits.WithLabelValues("sandbox_metadata")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheMisses.WithLabelValues("template_list")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheInvalidations.WithLabelValues("template_list")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.cacheL2Errors.WithLabelValues("template_list")))
}

func TestRecordCacheNoopBeforeInit(t *testing.T) {
	Reset()
	assert.NotPanics(t, func() { RecordCache("sandbox_metadata", "miss") })
}
