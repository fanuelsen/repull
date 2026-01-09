package updater

import (
	"fmt"

	"github.com/docker/docker/api/types/container"
)

const (
	// ComposeProjectLabel is the label set by Docker Compose for the project name
	ComposeProjectLabel = "com.docker.compose.project"
	// ComposeServiceLabel is the label set by Docker Compose for the service name
	ComposeServiceLabel = "com.docker.compose.service"
)

// GroupByComposeService groups containers by their compose project and service.
// Containers managed by Docker Compose are grouped together by project:service.
// Standalone containers (without compose labels) are grouped individually.
//
// Returns a map where:
// - Key format for compose containers: "project:service"
// - Key format for standalone containers: "standalone:containerID"
func GroupByComposeService(containers []container.InspectResponse) map[string][]container.InspectResponse {
	groups := make(map[string][]container.InspectResponse)

	for _, c := range containers {
		key := getGroupKey(c)
		groups[key] = append(groups[key], c)
	}

	return groups
}

// getGroupKey returns the group key for a container based on its labels.
func getGroupKey(c container.InspectResponse) string {
	if c.Config == nil || c.Config.Labels == nil {
		return fmt.Sprintf("standalone:%s", c.ID)
	}

	project, hasProject := c.Config.Labels[ComposeProjectLabel]
	service, hasService := c.Config.Labels[ComposeServiceLabel]

	// If both compose labels exist, group by project:service
	if hasProject && hasService && project != "" && service != "" {
		return fmt.Sprintf("%s:%s", project, service)
	}

	// Otherwise, treat as standalone
	return fmt.Sprintf("standalone:%s", c.ID)
}
