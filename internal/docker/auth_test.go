package docker

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/registry"
)

func TestRegistryDomain(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"nginx", "docker.io"},
		{"nginx:latest", "docker.io"},
		{"library/nginx", "docker.io"},
		{"fanuelsen/repull:v1.2.3", "docker.io"},
		{"ghcr.io/fanuelsen/repull:latest", "ghcr.io"},
		{"registry.example.com:5000/team/app", "registry.example.com:5000"},
		{"localhost:5000/app", "localhost:5000"},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got, err := registryDomain(tt.image)
			if err != nil {
				t.Fatalf("registryDomain(%q) error: %v", tt.image, err)
			}
			if got != tt.want {
				t.Errorf("registryDomain(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

// writeDockerConfig writes a config.json into a temp dir and points
// DOCKER_CONFIG at it for the duration of the test.
func writeDockerConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// decodeForTest decodes the RegistryAuth string back into an AuthConfig.
func decodeForTest(t *testing.T, encoded string) registry.AuthConfig {
	t.Helper()
	auth, err := registry.DecodeAuthConfig(encoded)
	if err != nil {
		t.Fatalf("failed to decode auth: %v", err)
	}
	return *auth
}

func TestRegistryAuthFor(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	t.Run("inline auth entry", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"ghcr.io": {"auth": "`+b64("user:secret")+`"}}}`)

		encoded := RegistryAuthFor("ghcr.io/fanuelsen/private:latest")
		if encoded == "" {
			t.Fatal("expected credentials, got none")
		}
		auth := decodeForTest(t, encoded)
		if auth.Username != "user" || auth.Password != "secret" {
			t.Errorf("got %s:%s, want user:secret", auth.Username, auth.Password)
		}
	})

	t.Run("docker hub legacy key", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"https://index.docker.io/v1/": {"auth": "`+b64("hubuser:hubpass")+`"}}}`)

		encoded := RegistryAuthFor("fanuelsen/private:latest")
		if encoded == "" {
			t.Fatal("expected credentials, got none")
		}
		auth := decodeForTest(t, encoded)
		if auth.Username != "hubuser" || auth.Password != "hubpass" {
			t.Errorf("got %s:%s, want hubuser:hubpass", auth.Username, auth.Password)
		}
	})

	t.Run("scheme-prefixed registry key", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"https://registry.example.com": {"auth": "`+b64("a:b")+`"}}}`)

		if RegistryAuthFor("registry.example.com/app") == "" {
			t.Error("expected credentials for scheme-prefixed key, got none")
		}
	})

	t.Run("username and password fields", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"ghcr.io": {"username": "u", "password": "p"}}}`)

		auth := decodeForTest(t, RegistryAuthFor("ghcr.io/x/y"))
		if auth.Username != "u" || auth.Password != "p" {
			t.Errorf("got %s:%s, want u:p", auth.Username, auth.Password)
		}
	})

	t.Run("password containing colons", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"ghcr.io": {"auth": "`+b64("user:pa:ss:word")+`"}}}`)

		auth := decodeForTest(t, RegistryAuthFor("ghcr.io/x/y"))
		if auth.Username != "user" || auth.Password != "pa:ss:word" {
			t.Errorf("got %s:%s, want user:pa:ss:word", auth.Username, auth.Password)
		}
	})

	t.Run("unpadded base64", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"ghcr.io": {"auth": "`+base64.RawStdEncoding.EncodeToString([]byte("u:p"))+`"}}}`)

		auth := decodeForTest(t, RegistryAuthFor("ghcr.io/x/y"))
		if auth.Username != "u" || auth.Password != "p" {
			t.Errorf("got %s:%s, want u:p", auth.Username, auth.Password)
		}
	})

	t.Run("no entry for registry", func(t *testing.T) {
		writeDockerConfig(t, `{"auths": {"ghcr.io": {"auth": "`+b64("u:p")+`"}}}`)

		if got := RegistryAuthFor("registry.example.com/app"); got != "" {
			t.Errorf("expected no credentials for unrelated registry, got %q", got)
		}
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Setenv("DOCKER_CONFIG", t.TempDir())

		if got := RegistryAuthFor("nginx"); got != "" {
			t.Errorf("expected no credentials without config file, got %q", got)
		}
	})

	t.Run("empty auth entry", func(t *testing.T) {
		// Credential-helper setups leave empty entries in auths.
		writeDockerConfig(t, `{"auths": {"ghcr.io": {}}, "credsStore": "desktop"}`)

		if got := RegistryAuthFor("ghcr.io/x/y"); got != "" {
			t.Errorf("expected no credentials from empty entry, got %q", got)
		}
	})

	t.Run("invalid image name", func(t *testing.T) {
		if got := RegistryAuthFor("UPPERCASE_NOT_VALID"); got != "" {
			t.Errorf("expected no credentials for invalid image name, got %q", got)
		}
	})
}
