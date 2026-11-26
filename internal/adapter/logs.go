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

package adapter

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSandboxNotFound = errors.New("sandbox not found")
	ErrLogsUnsupported = errors.New("sandbox logs not supported")
)

const (
	DefaultLogTailLines = 100
	MaxLogTailLines     = 1000
)

// LogOptions filters a snapshot of current sandbox logs.
type LogOptions struct {
	TailLines int64 // Zero selects the default; otherwise 1..MaxLogTailLines.
	SinceTime *time.Time
}

// LogOptionsReader optionally handles filtered requests without changing
// SandboxAdapter.GetLogs. A backend with no log support may return
// ErrLogsUnsupported from both methods, including for filtered requests.
type LogOptionsReader interface {
	GetLogsWithOptions(ctx context.Context, sandboxID string, opts LogOptions) ([]*LogEntry, error)
}
