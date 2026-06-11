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

// filterOutdatedContainers returns the containers whose image ID differs from
// latestID, i.e. containers not running the image their tag currently points to.
func filterOutdatedContainers(containers []container.InspectResponse, latestID string) []container.InspectResponse {
	var outdated []container.InspectResponse

	for _, c := range containers {
		if c.Image != latestID {
			outdated = append(outdated, c)
		}
	}

	return outdated
}
