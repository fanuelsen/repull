package docker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/registry"
)

// dockerConfig models the subset of Docker's config.json we need.
type dockerConfig struct {
	Auths       map[string]dockerConfigAuth `json:"auths"`
	CredsStore  string                      `json:"credsStore"`
	CredHelpers map[string]string           `json:"credHelpers"`
}

type dockerConfigAuth struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
}

// credHelperWarn makes sure the unsupported-credential-helper warning is
// logged once instead of on every pull of every cycle.
var credHelperWarn sync.Once

// RegistryAuthFor returns encoded credentials for the registry hosting
// imageName, suitable for image.PullOptions.RegistryAuth. Returns "" when no
// credentials are configured, which is fine for public images.
//
// The Docker daemon does not store registry credentials: `docker login`
// saves them client-side in config.json, and every API client must send
// them along with each pull request. Repull therefore reads the same file —
// $DOCKER_CONFIG/config.json, falling back to ~/.docker/config.json.
//
// Only inline base64 "auth" (or username/password) entries are supported.
// Credential helpers (credsStore/credHelpers) would require the docker/cli
// dependency and are deliberately not supported.
func RegistryAuthFor(imageName string) string {
	domain, err := registryDomain(imageName)
	if err != nil {
		return ""
	}

	cfg, err := loadDockerConfig()
	if err != nil {
		// A missing config file is normal when no registry needs auth.
		if !os.IsNotExist(err) {
			log.Printf("[WARN] Failed to read Docker config: %v", err)
		}
		return ""
	}

	entry, ok := lookupAuth(cfg, domain)
	if !ok {
		if cfg.CredsStore != "" || cfg.CredHelpers[domain] != "" {
			credHelperWarn.Do(func() {
				log.Printf("[WARN] Docker config uses a credential helper, which repull does not support; pulls will be unauthenticated unless config.json contains inline auths")
			})
		}
		return ""
	}

	auth := registry.AuthConfig{
		ServerAddress: domain,
		Username:      entry.Username,
		Password:      entry.Password,
		IdentityToken: entry.IdentityToken,
	}

	// The "auth" field is base64(username:password) and takes precedence,
	// matching the Docker CLI's behavior.
	if entry.Auth != "" {
		if user, pass, ok := decodeAuthField(entry.Auth); ok {
			auth.Username = user
			auth.Password = pass
		}
	}

	if auth.Username == "" && auth.IdentityToken == "" {
		return ""
	}

	encoded, err := registry.EncodeAuthConfig(auth)
	if err != nil {
		log.Printf("[WARN] Failed to encode registry credentials for %s: %v", domain, err)
		return ""
	}
	return encoded
}

// registryDomain extracts the registry host from an image reference.
// Bare names like "nginx" or "library/nginx" normalize to "docker.io".
func registryDomain(imageName string) (string, error) {
	named, err := reference.ParseNormalizedNamed(imageName)
	if err != nil {
		return "", err
	}
	return reference.Domain(named), nil
}

// loadDockerConfig reads $DOCKER_CONFIG/config.json, falling back to
// ~/.docker/config.json — the same lookup order as the Docker CLI.
func loadDockerConfig() (*dockerConfig, error) {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".docker")
	}

	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &cfg, nil
}

// lookupAuth finds the auths entry for a registry domain. Docker Hub
// credentials are stored under a legacy key, and other registries may be
// keyed with or without a scheme prefix.
func lookupAuth(cfg *dockerConfig, domain string) (dockerConfigAuth, bool) {
	var keys []string
	if domain == "docker.io" {
		keys = []string{"https://index.docker.io/v1/", "index.docker.io", "docker.io"}
	} else {
		keys = []string{domain, "https://" + domain, "http://" + domain}
	}

	for _, key := range keys {
		if entry, ok := cfg.Auths[key]; ok {
			return entry, true
		}
	}
	return dockerConfigAuth{}, false
}

// decodeAuthField decodes a base64 "auth" entry into username and password.
// Tolerates missing padding, which older Docker versions wrote.
func decodeAuthField(auth string) (string, string, bool) {
	decoded, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(auth)
		if err != nil {
			return "", "", false
		}
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok || user == "" {
		return "", "", false
	}
	return user, pass, true
}
