package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// TestRecreatePortConfigDropsPortsForContainerNetns verifies that a container
// sharing another container's network namespace (network_mode: container:/
// service:) gets no exposed/published ports, even though its inspect response
// still reports the image's EXPOSE. Copying those would make the daemon reject
// the create with "conflicting options: port exposing and the container type
// network mode".
func TestRecreatePortConfigDropsPortsForContainerNetns(t *testing.T) {
	cfg := &container.Config{
		ExposedPorts: nat.PortSet{"8989/tcp": struct{}{}},
	}
	host := &container.HostConfig{
		NetworkMode:     "container:abc123",
		PublishAllPorts: true,
		PortBindings:    nat.PortMap{"8989/tcp": []nat.PortBinding{{HostPort: "8989"}}},
	}

	exposed, bindings, publishAll := recreatePortConfig(cfg, host)

	if len(exposed) != 0 {
		t.Errorf("exposed = %v, want empty for container netns", exposed)
	}
	if len(bindings) != 0 {
		t.Errorf("bindings = %v, want empty for container netns", bindings)
	}
	if publishAll {
		t.Errorf("publishAll = true, want false for container netns")
	}
}

// TestRecreatePortConfigKeepsPortsForBridge verifies that normal (bridge)
// containers still carry their exposed and published ports, including
// image/compose "expose:" entries that have no host binding.
func TestRecreatePortConfigKeepsPortsForBridge(t *testing.T) {
	cfg := &container.Config{
		ExposedPorts: nat.PortSet{"443/tcp": struct{}{}, "8080/tcp": struct{}{}},
	}
	host := &container.HostConfig{
		NetworkMode:     "bridge",
		PublishAllPorts: true,
		PortBindings:    nat.PortMap{"443/tcp": []nat.PortBinding{{HostPort: "443"}}},
	}

	exposed, bindings, publishAll := recreatePortConfig(cfg, host)

	if _, ok := exposed["8080/tcp"]; !ok {
		t.Errorf("exposed missing image-exposed 8080/tcp: %v", exposed)
	}
	if _, ok := bindings["443/tcp"]; !ok {
		t.Errorf("bindings missing published 443/tcp: %v", bindings)
	}
	if !publishAll {
		t.Errorf("publishAll = false, want true for bridge")
	}
}

// TestIsSelfUpdateLeftover verifies that startup cleanup only matches
// containers carrying the exact rename fingerprint self-update produces —
// "<name>-old-<its own short ID>". Anything else (a normal repull instance,
// a container whose image merely bakes in the io.repull.app label, a suffix
// borrowed from a different container's ID) must not match, since matching
// containers are force-removed.
func TestIsSelfUpdateLeftover(t *testing.T) {
	id := "abcdef123456789012345678901234567890"
	short := ShortID(id) // abcdef123456

	tests := []struct {
		name          string
		containerName string
		id            string
		want          bool
	}{
		{name: "renamed leftover", containerName: "repull-old-" + short, id: id, want: true},
		{name: "normal instance name", containerName: "repull", id: id, want: false},
		{name: "suffix from another container's ID", containerName: "repull-old-000000000000", id: id, want: false},
		{name: "suffix only, empty original name", containerName: "-old-" + short, id: id, want: false},
		{name: "empty name", containerName: "", id: id, want: false},
		{name: "old marker without ID", containerName: "repull-old-", id: id, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelfUpdateLeftover(tt.containerName, tt.id); got != tt.want {
				t.Errorf("isSelfUpdateLeftover(%q, %q) = %v, want %v", tt.containerName, tt.id, got, tt.want)
			}
		})
	}
}

// TestLooksLikeContainerID verifies the guard that decides whether a
// network_mode ref may be prefix-matched against container IDs. Short hex-like
// names ("db", "cafe") must be treated as names: prefix-matching them against
// IDs could rewire a container into an unrelated network namespace.
func TestLooksLikeContainerID(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{ref: "abcdef123456", want: true}, // short ID
		{ref: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", want: true}, // full ID
		{ref: "db", want: false},              // hex-like name, too short
		{ref: "cafe", want: false},            // hex-like name, too short
		{ref: "my-service-name", want: false}, // name with non-hex chars
		{ref: "abcdef12345", want: false},     // 11 chars, below short-ID length
		{ref: "ABCDEF123456", want: false},    // uppercase is not an ID
		{ref: "abcdef12345g", want: false},    // non-hex char
		{ref: "", want: false},
	}

	for _, tt := range tests {
		if got := looksLikeContainerID(tt.ref); got != tt.want {
			t.Errorf("looksLikeContainerID(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

// TestContainerOnlyLabels verifies that labels inherited unchanged from the
// image are dropped (so the new image's labels apply fresh), while labels the
// user set on the container — including overrides of image labels — are kept.
func TestContainerOnlyLabels(t *testing.T) {
	merged := map[string]string{
		"io.repull.enable":           "true",  // baked into the image
		"com.docker.compose.project": "myapp", // set by compose
		"maintainer":                 "me",    // container overrides image value
	}
	image := map[string]string{
		"io.repull.enable": "true",
		"maintainer":       "upstream",
	}

	got := containerOnlyLabels(merged, image)

	if _, ok := got["io.repull.enable"]; ok {
		t.Errorf("image-inherited label not dropped: %v", got)
	}
	if got["com.docker.compose.project"] != "myapp" {
		t.Errorf("container-set label lost: %v", got)
	}
	if got["maintainer"] != "me" {
		t.Errorf("container override of image label lost: %v", got)
	}

	// Nil/empty image labels: merged map returned unchanged.
	if got := containerOnlyLabels(merged, nil); len(got) != len(merged) {
		t.Errorf("with nil image labels got %v, want all of %v", got, merged)
	}
}

// TestContainerOnlyEnv verifies that env entries inherited unchanged from the
// image are dropped, while container-set entries (new vars, or overrides with
// a different value) are kept.
func TestContainerOnlyEnv(t *testing.T) {
	merged := []string{
		"PATH=/usr/local/bin:/usr/bin", // from the image
		"APP_VERSION=1.2.3",            // from the image
		"DB_HOST=db",                   // set on the container
		"LOG_LEVEL=debug",              // overrides the image's value
	}
	image := []string{
		"PATH=/usr/local/bin:/usr/bin",
		"APP_VERSION=1.2.3",
		"LOG_LEVEL=info",
	}

	got := containerOnlyEnv(merged, image)

	want := []string{"DB_HOST=db", "LOG_LEVEL=debug"}
	if len(got) != len(want) {
		t.Fatalf("containerOnlyEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("containerOnlyEnv[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Nil/empty image env: merged returned unchanged.
	if got := containerOnlyEnv(merged, nil); len(got) != len(merged) {
		t.Errorf("with nil image env got %v, want all of %v", got, merged)
	}
}

func TestSanitizeEndpoint(t *testing.T) {
	oldContainerID := "abcdef123456789012345678901234567890"
	oldShort := ShortID(oldContainerID)

	t.Run("nil endpoint", func(t *testing.T) {
		if got := sanitizeEndpoint(nil, oldContainerID); got != nil {
			t.Errorf("sanitizeEndpoint(nil) = %v, want nil", got)
		}
	})

	t.Run("keeps user config, drops runtime state", func(t *testing.T) {
		old := &network.EndpointSettings{
			IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: "172.20.0.5"},
			Links:      []string{"db:db"},
			DriverOpts: map[string]string{"opt": "val"},
			Aliases:    []string{"web", oldShort},
			// Runtime state that must not be copied
			EndpointID: "ep-runtime-id",
			NetworkID:  "net-runtime-id",
			IPAddress:  "172.20.0.5",
			Gateway:    "172.20.0.1",
			MacAddress: "02:42:ac:14:00:05",
		}

		got := sanitizeEndpoint(old, oldContainerID)

		if got.IPAMConfig == nil || got.IPAMConfig.IPv4Address != "172.20.0.5" {
			t.Errorf("IPAMConfig not preserved: %+v", got.IPAMConfig)
		}
		if len(got.Links) != 1 || got.Links[0] != "db:db" {
			t.Errorf("Links not preserved: %v", got.Links)
		}
		if got.DriverOpts["opt"] != "val" {
			t.Errorf("DriverOpts not preserved: %v", got.DriverOpts)
		}
		if len(got.Aliases) != 1 || got.Aliases[0] != "web" {
			t.Errorf("Aliases = %v, want [web] (old short-ID alias dropped)", got.Aliases)
		}
		if got.EndpointID != "" || got.NetworkID != "" || got.IPAddress != "" ||
			got.Gateway != "" || got.MacAddress != "" {
			t.Errorf("runtime state copied: %+v", got)
		}
	})

	t.Run("no aliases", func(t *testing.T) {
		got := sanitizeEndpoint(&network.EndpointSettings{}, oldContainerID)
		if len(got.Aliases) != 0 {
			t.Errorf("Aliases = %v, want empty", got.Aliases)
		}
	})
}
