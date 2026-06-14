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
