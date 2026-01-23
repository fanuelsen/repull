package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/fanuelsen/repull/internal/docker"
	"github.com/fanuelsen/repull/internal/notify"
)

// UpdateGroups processes each group of containers and updates them if their image digest has changed.
// It updates one group at a time (sequential, not parallel) for safety.
func UpdateGroups(ctx context.Context, cli *client.Client, groups map[string][]container.InspectResponse, dryRun bool, notifier *notify.Notifier) error {
	for groupKey, containers := range groups {
		if len(containers) == 0 {
			continue
		}

		log.Printf("[INFO] Checking %s (%d container(s))", groupKey, len(containers))

		// Get image name from first container (all containers in a group share the same image)
		imageName := containers[0].Config.Image

		// Get current digest before pulling
		oldDigest, err := docker.GetImageDigest(ctx, cli, imageName)
		if err != nil {
			log.Printf("[WARN] Failed to get current digest for %s: %v", imageName, err)
			// Continue with empty digest - will trigger update after pull
			oldDigest = ""
		}

		// Pull latest image
		log.Printf("[INFO] Pulling image %s", imageName)
		if err := docker.PullImage(ctx, cli, imageName); err != nil {
			log.Printf("[ERROR] Failed to pull image %s: %v", imageName, err)
			if notifier != nil {
				notifier.SendError(groupKey, fmt.Sprintf("Failed to pull image %s: %v", imageName, err))
			}
			return fmt.Errorf("failed to pull image %s: %w", imageName, err)
		}

		// Get new digest after pulling
		newDigest, err := docker.GetImageDigest(ctx, cli, imageName)
		if err != nil {
			log.Printf("[ERROR] Failed to get new digest for %s: %v", imageName, err)
			if notifier != nil {
				notifier.SendError(groupKey, fmt.Sprintf("Failed to get digest for %s: %v", imageName, err))
			}
			return fmt.Errorf("failed to get digest for %s: %w", imageName, err)
		}

		// Check if digest changed
		if !docker.HasDigestChanged(oldDigest, newDigest) {
			log.Printf("[INFO] Image digest unchanged, skipping %s", groupKey)
			continue
		}

		// Digest changed - recreate containers
		log.Printf("[INFO] Image digest changed: %s -> %s", truncateDigest(oldDigest), truncateDigest(newDigest))

		if dryRun {
			log.Printf("[DRY-RUN] Would recreate %s (%d container(s))", groupKey, len(containers))
			continue
		}

		// Recreate all containers in the group
		log.Printf("[INFO] Recreating %d container(s)", len(containers))
		for _, c := range containers {
			containerName := strings.TrimPrefix(c.Name, "/")
			if containerName == "" {
				containerName = c.ID[:12]
			}

			// Self-update: exit and let Docker restart policy handle it
			if isSelf(c.ID) {
				log.Printf("[INFO] Self-update detected for %s, exiting to restart with new image", containerName)
				if notifier != nil {
					notifier.SendUpdate(groupKey, imageName, oldDigest, newDigest)
				}
				os.Exit(0)
			}

			log.Printf("[INFO] Recreating container %s", containerName)
			if err := docker.RecreateContainer(ctx, cli, c); err != nil {
				log.Printf("[ERROR] Failed to recreate container %s: %v", containerName, err)
				if notifier != nil {
					notifier.SendError(groupKey, fmt.Sprintf("Failed to recreate container %s: %v", containerName, err))
				}
				return fmt.Errorf("failed to recreate container %s: %w", containerName, err)
			}
			log.Printf("[INFO] Successfully recreated %s", containerName)
		}

		// Send success notification after all containers in group are recreated
		if notifier != nil {
			notifier.SendUpdate(groupKey, imageName, oldDigest, newDigest)
		}
	}

	return nil
}

// truncateDigest shortens a digest string for logging.
// Example: sha256:abc123... -> sha256:abc123
func truncateDigest(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "..."
	}
	return digest
}

// isSelf checks if the given container ID belongs to this running instance.
// Docker sets the hostname to the first 12 characters of the container ID by default.
func isSelf(containerID string) bool {
	hostname, err := os.Hostname()
	if err != nil {
		return false
	}
	return strings.HasPrefix(containerID, hostname)
}
