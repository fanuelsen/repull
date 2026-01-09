package updater

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestFilterOptedInContainers(t *testing.T) {
	tests := []struct {
		name       string
		containers []container.InspectResponse
		want       int
	}{
		{
			name:       "empty list",
			containers: []container.InspectResponse{},
			want:       0,
		},
		{
			name: "all opted in",
			containers: []container.InspectResponse{
				{
					Config: &container.Config{
						Labels: map[string]string{EnableLabel: "true"},
					},
				},
				{
					Config: &container.Config{
						Labels: map[string]string{EnableLabel: "true"},
					},
				},
			},
			want: 2,
		},
		{
			name: "none opted in",
			containers: []container.InspectResponse{
				{
					Config: &container.Config{
						Labels: map[string]string{"other": "label"},
					},
				},
				{
					Config: &container.Config{
						Labels: map[string]string{},
					},
				},
			},
			want: 0,
		},
		{
			name: "mixed",
			containers: []container.InspectResponse{
				{
					Config: &container.Config{
						Labels: map[string]string{EnableLabel: "true"},
					},
				},
				{
					Config: &container.Config{
						Labels: map[string]string{EnableLabel: "false"},
					},
				},
				{
					Config: &container.Config{
						Labels: map[string]string{"other": "label"},
					},
				},
			},
			want: 1,
		},
		{
			name: "nil config",
			containers: []container.InspectResponse{
				{
					Config: nil,
				},
			},
			want: 0,
		},
		{
			name: "nil labels",
			containers: []container.InspectResponse{
				{
					Config: &container.Config{
						Labels: nil,
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterOptedInContainers(tt.containers)
			if len(got) != tt.want {
				t.Errorf("FilterOptedInContainers() returned %d containers, want %d", len(got), tt.want)
			}
		})
	}
}
