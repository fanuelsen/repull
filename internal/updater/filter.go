package updater

import (
	"github.com/docker/docker/api/types/container"
)

const (
	// EnableLabel is the label that must be set to "true" for a container to be auto-updated
	EnableLabel = "io.repull.enable"
)

// FilterOptedInContainers returns only containers that have the io.repull.enable=true label.
func FilterOptedInContainers(containers []container.InspectResponse) []container.InspectResponse {
	var filtered []container.InspectResponse

	for _, c := range containers {
		if c.Config != nil && c.Config.Labels != nil {
			if value, exists := c.Config.Labels[EnableLabel]; exists && value == "true" {
				filtered = append(filtered, c)
			}
		}
	}

	return filtered
}
