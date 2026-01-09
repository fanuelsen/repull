package updater

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestGroupByComposeService(t *testing.T) {
	tests := []struct {
		name       string
		containers []container.InspectResponse
		wantGroups int
		wantKeys   []string
	}{
		{
			name:       "empty list",
			containers: []container.InspectResponse{},
			wantGroups: 0,
			wantKeys:   []string{},
		},
		{
			name: "compose containers same service",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
							ComposeServiceLabel: "web",
						},
					},
				},
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "def456",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
							ComposeServiceLabel: "web",
						},
					},
				},
			},
			wantGroups: 1,
			wantKeys:   []string{"myapp:web"},
		},
		{
			name: "compose containers different services",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
							ComposeServiceLabel: "web",
						},
					},
				},
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "def456",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
							ComposeServiceLabel: "db",
						},
					},
				},
			},
			wantGroups: 2,
			wantKeys:   []string{"myapp:web", "myapp:db"},
		},
		{
			name: "standalone containers",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: &container.Config{
						Labels: map[string]string{},
					},
				},
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "def456",
					},
					Config: &container.Config{
						Labels: map[string]string{},
					},
				},
			},
			wantGroups: 2,
			wantKeys:   []string{"standalone:abc123", "standalone:def456"},
		},
		{
			name: "mixed compose and standalone",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
							ComposeServiceLabel: "web",
						},
					},
				},
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "def456",
					},
					Config: &container.Config{
						Labels: map[string]string{},
					},
				},
			},
			wantGroups: 2,
			wantKeys:   []string{"myapp:web", "standalone:def456"},
		},
		{
			name: "nil config",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: nil,
				},
			},
			wantGroups: 1,
			wantKeys:   []string{"standalone:abc123"},
		},
		{
			name: "partial compose labels",
			containers: []container.InspectResponse{
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "abc123",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeProjectLabel: "myapp",
						},
					},
				},
				{
					ContainerJSONBase: &container.ContainerJSONBase{
						ID: "def456",
					},
					Config: &container.Config{
						Labels: map[string]string{
							ComposeServiceLabel: "web",
						},
					},
				},
			},
			wantGroups: 2,
			wantKeys:   []string{"standalone:abc123", "standalone:def456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := GroupByComposeService(tt.containers)

			if len(groups) != tt.wantGroups {
				t.Errorf("GroupByComposeService() returned %d groups, want %d", len(groups), tt.wantGroups)
			}

			for _, key := range tt.wantKeys {
				if _, exists := groups[key]; !exists {
					t.Errorf("GroupByComposeService() missing expected key: %s", key)
				}
			}
		})
	}
}

func TestGetGroupKey(t *testing.T) {
	tests := []struct {
		name      string
		container container.InspectResponse
		want      string
	}{
		{
			name: "compose container",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "abc123",
				},
				Config: &container.Config{
					Labels: map[string]string{
						ComposeProjectLabel: "myapp",
						ComposeServiceLabel: "web",
					},
				},
			},
			want: "myapp:web",
		},
		{
			name: "standalone container",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "abc123",
				},
				Config: &container.Config{
					Labels: map[string]string{},
				},
			},
			want: "standalone:abc123",
		},
		{
			name: "nil config",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "abc123",
				},
				Config: nil,
			},
			want: "standalone:abc123",
		},
		{
			name: "empty project",
			container: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: "abc123",
				},
				Config: &container.Config{
					Labels: map[string]string{
						ComposeProjectLabel: "",
						ComposeServiceLabel: "web",
					},
				},
			},
			want: "standalone:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getGroupKey(tt.container)
			if got != tt.want {
				t.Errorf("getGroupKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
