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
// To update: check out the desired upstream commit, copy the generated
// env.pb.go, env_grpc.pb.go, guest.pb.go and guest_grpc.pb.go from its
// proto/ateenv/v1alpha Go output, rename the package clause to ateenvpb,
// bump the commit above, and run the adapter tests. Do not edit the
// generated files by hand.
package ateenvpb
