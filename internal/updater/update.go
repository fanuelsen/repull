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
	// Track containers recreated during this update cycle.
	// This is used to resolve stale network_mode references when containers
	// use network_mode: service:X (which Docker stores as container:<id>).
	recreated := make(docker.RecreatedContainers)

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

			// Self-update: rename current container, create new one with same name, start it, then exit
			if isSelf(c.ID) {
				log.Printf("[INFO] Self-update detected for %s", containerName)

				// Rename current container to allow new container to use the name
				tempName := containerName + "-old-" + c.ID[:8]
				if err := cli.ContainerRename(ctx, c.ID, tempName); err != nil {
					log.Printf("[ERROR] Failed to rename container for self-update: %v", err)
					if notifier != nil {
						notifier.SendError(groupKey, fmt.Sprintf("Self-update failed: %v", err))
					}
					return fmt.Errorf("failed to rename container for self-update: %w", err)
				}
				log.Printf("[INFO] Renamed %s to %s", containerName, tempName)

				// Create and start new container with original name
				if err := docker.CreateAndStartContainer(ctx, cli, c, containerName); err != nil {
					// Rollback: rename back to original
					log.Printf("[ERROR] Failed to create new container, rolling back: %v", err)
					cli.ContainerRename(ctx, c.ID, containerName)
					if notifier != nil {
						notifier.SendError(groupKey, fmt.Sprintf("Self-update failed: %v", err))
					}
					return fmt.Errorf("failed to create new container for self-update: %w", err)
				}

				log.Printf("[INFO] New container started, old container will stop on exit")
				if notifier != nil {
					notifier.SendUpdate(groupKey, imageName, oldDigest, newDigest)
				}

				// Exit - this stops the old (renamed) container, new one is already running
				os.Exit(0)
			}

			log.Printf("[INFO] Recreating container %s", containerName)
			newID, err := docker.RecreateContainer(ctx, cli, c, recreated)
			if err != nil {
				log.Printf("[ERROR] Failed to recreate container %s: %v", containerName, err)
				if notifier != nil {
					notifier.SendError(groupKey, fmt.Sprintf("Failed to recreate container %s: %v", containerName, err))
				}
				return fmt.Errorf("failed to recreate container %s: %w", containerName, err)
			}
			// Track the old->new ID mapping for resolving network_mode references
			recreated[c.ID] = newID
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
