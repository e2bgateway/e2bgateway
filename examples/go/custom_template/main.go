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

// Backend: mock, or OpenSandbox with template creation, aliases and tags.
// Run: go run ./examples/go/custom_template
// Expected: Build status and the new template's tag before cleanup.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/e2bgateway/e2bgateway/examples/go/internal/example"
	"github.com/e2bgateway/e2bgateway/pkg/client"
)

func main() { example.Main(buildTemplate) }

func buildTemplate(ctx context.Context, c *client.Client) (err error) {
	name := fmt.Sprintf("go-example-%d", time.Now().UnixNano())
	build, err := c.CreateTemplate(ctx, client.TemplateBuildRequest{Name: name, Dockerfile: "FROM python:3.11-slim"})
	if err != nil {
		return err
	}
	if build.TemplateID == "" {
		return errors.New("missing template ID")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = errors.Join(err, c.DeleteTemplate(cleanup, build.TemplateID))
	}()
	if build.BuildID == "" {
		return errors.New("missing build ID")
	}
	if err := waitReady(ctx, c, build); err != nil {
		return err
	}
	if err := c.CreateTemplateAlias(ctx, build.TemplateID, name+"-alias"); err != nil {
		return err
	}
	if _, err := c.CreateTemplateTag(ctx, build.TemplateID, "example", build.BuildID); err != nil {
		return err
	}
	tags, err := c.ListTemplateTags(ctx, build.TemplateID)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if tag.Name == "example" && tag.BuildID == build.BuildID {
			fmt.Printf("Tag: %s (%s)\n", tag.Name, tag.BuildID)
			return nil
		}
	}
	return errors.New("missing build tag")
}

func waitReady(ctx context.Context, c *client.Client, build client.TemplateBuildResponse) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := c.GetBuildStatus(ctx, build.TemplateID, build.BuildID)
		if err != nil {
			return err
		}
		switch status.Status {
		case "ready":
			fmt.Printf("Build ready: %s\n", status.BuildID)
			return nil
		case "error":
			return fmt.Errorf("template build failed: %s", status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
