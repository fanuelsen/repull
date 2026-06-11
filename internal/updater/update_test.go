package updater

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestIsRepullInstance(t *testing.T) {
	tests := []struct {
		name      string
		container container.InspectResponse
		want      bool
	}{
		{
			name: "repull label set",
			container: container.InspectResponse{
				Config: &container.Config{
					Labels: map[string]string{"io.repull.app": "true"},
				},
			},
			want: true,
		},
		{
			name: "label set to false",
			container: container.InspectResponse{
				Config: &container.Config{
					Labels: map[string]string{"io.repull.app": "false"},
				},
			},
			want: false,
		},
		{
			name: "no labels",
			container: container.InspectResponse{
				Config: &container.Config{Labels: map[string]string{}},
			},
			want: false,
		},
		{
			name:      "nil config",
			container: container.InspectResponse{Config: nil},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRepullInstance(tt.container); got != tt.want {
				t.Errorf("isRepullInstance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSelfContainer(t *testing.T) {
	fullID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	tests := []struct {
		name      string
		container container.InspectResponse
		hostname  string
		want      bool
	}{
		{
			name: "hostname is short container ID",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: fullID, Name: "/repull"},
			},
			hostname: fullID[:12],
			want:     true,
		},
		{
			name: "custom hostname matches container name",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: fullID, Name: "/repull"},
			},
			hostname: "repull",
			want:     true,
		},
		{
			name: "host machine hostname matches nothing",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: fullID, Name: "/repull"},
			},
			hostname: "my-server",
			want:     false,
		},
		{
			name: "different container's short ID",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: fullID, Name: "/repull-two"},
			},
			hostname: "123456abcdef",
			want:     false,
		},
		{
			name: "empty hostname",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: fullID, Name: "/repull"},
			},
			hostname: "",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelfContainer(tt.container, tt.hostname); got != tt.want {
				t.Errorf("isSelfContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}
