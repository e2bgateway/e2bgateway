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

package agentsubstrate

import (
	"crypto/tls"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
	"github.com/e2bgateway/e2bgateway/internal/adapter/util"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// NewAdapterFromConfig creates an Agent Substrate adapter from BackendConfig.
func NewAdapterFromConfig(bcfg config.BackendConfig, registry *adapter.Registry) (adapter.SandboxAdapter, error) {
	cfg := Config{Name: bcfg.Name, Registry: registry}

	cfg.Endpoint = util.StringVal(bcfg.Config, "endpoint")
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required for the agent-substrate adapter")
	}
	cfg.Atespace = util.StringVal(bcfg.Config, "atespace")
	cfg.DefaultTemplate = util.StringVal(bcfg.Config, "defaulttemplate", "defaultTemplate")
	cfg.TemplateToActor = util.LookupStringMap(bcfg.Config, "templatetoactortemplate", "templateToActorTemplate")
	if value, ok := bcfg.Config["tls"]; ok {
		enabled, valid := value.(bool)
		if !valid {
			return nil, fmt.Errorf("backend %q: tls must be a boolean", bcfg.Name)
		}
		cfg.TLS = enabled
	}

	return New(cfg)
}

func dial(cfg Config) (*grpc.ClientConn, error) {
	creds := insecure.NewCredentials()
	if cfg.TLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(cfg.Endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("connecting to ate-env-api %s: %w", cfg.Endpoint, err)
	}
	return conn, nil
}
