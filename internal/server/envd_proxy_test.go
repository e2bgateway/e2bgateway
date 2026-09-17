package server

import "testing"

func TestExtractSandboxIDFromHost(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		domain string
		want   string
	}{
		{
			name:   "port-sandboxID pattern",
			host:   "8080-abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "sandboxID only pattern",
			host:   "abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "sandbox prefix returns empty",
			host:   "sandbox.example.com",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "bare domain returns domain as prefix",
			// When host equals domain, TrimSuffix(".domain") doesn't match
			// so the full host is returned as the prefix (no dash → returned as-is).
			host:   "example.com",
			domain: "example.com",
			want:   "example.com",
		},
		{
			name:   "different domain returns empty",
			host:   "abc123.other.com",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "host with port stripped",
			host:   "8080-abc123.example.com:443",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "empty domain returns empty",
			host:   "8080-abc123.example.com",
			domain: "",
			want:   "",
		},
		{
			name:   "multiple dashes in ID",
			host:   "8080-abc-123-def.example.com",
			domain: "example.com",
			want:   "abc-123-def",
		},
		{
			name:   "nested subdomain extracts after first dash",
			// prefix = "foo.bar-abc123", first dash at index 7 → returns "abc123"
			host:   "foo.bar-abc123.example.com",
			domain: "example.com",
			want:   "abc123",
		},
		{
			name:   "empty host returns empty",
			host:   "",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "port only sandboxID",
			host:   "49983-xyz789.example.com",
			domain: "example.com",
			want:   "xyz789",
		},
		{
			name:   "sandbox with port returns empty",
			host:   "sandbox.example.com:443",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "no dash means entire prefix is sandbox ID",
			host:   "mySandbox123.example.com",
			domain: "example.com",
			want:   "mySandbox123",
		},
		{
			name:   "host not matching domain at all",
			host:   "completely-different.org",
			domain: "example.com",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSandboxIDFromHost(tt.host, tt.domain)
			if got != tt.want {
				t.Errorf("extractSandboxIDFromHost(%q, %q) = %q, want %q", tt.host, tt.domain, got, tt.want)
			}
		})
	}
}

func TestSingleJoiningSlash(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want string
	}{
		{
			name: "both have slash",
			a:    "a/",
			b:    "/b",
			want: "a/b",
		},
		{
			name: "neither has slash",
			a:    "a",
			b:    "b",
			want: "a/b",
		},
		{
			name: "first has trailing slash only",
			a:    "a/",
			b:    "b",
			want: "a/b",
		},
		{
			name: "second has leading slash only",
			a:    "a",
			b:    "/b",
			want: "a/b",
		},
		{
			name: "empty first",
			a:    "",
			b:    "/b",
			want: "/b",
		},
		{
			name: "empty second",
			a:    "a",
			b:    "",
			want: "a/",
		},
		{
			name: "both slashes",
			a:    "/",
			b:    "/",
			want: "/",
		},
		{
			name: "both empty",
			a:    "",
			b:    "",
			want: "/",
		},
		{
			name: "real paths",
			a:    "/api/v1/proxy/49983",
			b:    "/process.Process/Start",
			want: "/api/v1/proxy/49983/process.Process/Start",
		},
		{
			name: "real paths with trailing slash on base",
			a:    "/api/v1/proxy/49983/",
			b:    "/process.Process/Start",
			want: "/api/v1/proxy/49983/process.Process/Start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := singleJoiningSlash(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("singleJoiningSlash(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
