package docker

import (
	"testing"

	"github.com/docker/docker/api/types/network"
)

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
