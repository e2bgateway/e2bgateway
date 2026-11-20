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

// Package ateenvpb holds the Go bindings for the Agent Substrate ate-env
// protos (github.com/agent-substrate/env, Apache-2.0), vendored from
// proto/ateenv/v1alpha so the gateway need not import the upstream module
// (which requires a newer Go toolchain).
//
// Source: github.com/agent-substrate/env @ 56424de763f91daa6b6c38f6249bacef1d82e5e1
// (proto/ateenv/v1alpha/{env,guest}.proto), generated with protoc-gen-go
// v1.36.11 and protoc-gen-go-grpc v1.5.1. The only local change is the Go
// package name (renamed to ateenvpb).
//
// To update, run hack/update-ateenvpb.sh [git-ref] (it re-downloads the four
// generated files, renames the package clause, and bumps the commit above),
// then run the adapter tests. Do not edit the generated files by hand.
package ateenvpb
