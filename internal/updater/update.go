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

		log.Printf("[INFO] Checking %s (%d container(s))", sanitize(groupKey), len(containers))

		// Get image name from first container (all containers in a group share the same image)
		imageName := containers[0].Config.Image

		// Pull latest image
		log.Printf("[INFO] Pulling image %s", sanitize(imageName))
		if err := docker.PullImage(ctx, cli, imageName); err != nil {
			log.Printf("[ERROR] Failed to pull image %s: %v", sanitize(imageName), err)
			if notifier != nil {
				notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to pull image %s: %v", sanitize(imageName), err))
			}
			return fmt.Errorf("failed to pull image %s: %w", imageName, err)
		}

		// Resolve the image ID the tag points to after the pull
		latestID, err := docker.GetImageID(ctx, cli, imageName)
		if err != nil {
			log.Printf("[ERROR] Failed to inspect image %s: %v", sanitize(imageName), err)
			if notifier != nil {
				notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to inspect image %s: %v", sanitize(imageName), err))
			}
			return fmt.Errorf("failed to inspect image %s: %w", imageName, err)
		}

		// Compare each container's image ID against the latest. Unlike comparing
		// the tag's digest before/after the pull, this detects outdated containers
		// even when the image was already pulled earlier — by a dry run, a manual
		// docker pull, or a cycle that pulled successfully but failed to recreate.
		outdated := filterOutdatedContainers(containers, latestID)
		if len(outdated) == 0 {
			log.Printf("[INFO] Already running latest image, skipping %s", sanitize(groupKey))
			continue
		}

		oldID := outdated[0].Image
		log.Printf("[INFO] Image updated: %s -> %s", truncateDigest(oldID), truncateDigest(latestID))

		if dryRun {
			log.Printf("[DRY-RUN] Would recreate %s (%d container(s))", sanitize(groupKey), len(outdated))
			continue
		}

		// Recreate the outdated containers in the group
		log.Printf("[INFO] Recreating %d container(s)", len(outdated))
		for _, c := range outdated {
			containerName := strings.TrimPrefix(c.Name, "/")
			if containerName == "" {
				if len(c.ID) > 12 {
					containerName = c.ID[:12]
				} else {
					containerName = c.ID
				}
			}

			// Self-update: container already passed the io.repull.enable=true filter,
			// so the user has opted in. Use the rename-based self-update flow.
			if isSelf(c) {
				log.Printf("[INFO] Self-update detected for %s", sanitize(containerName))

				// Rename current container to allow new container to use the name
				suffix := c.ID
				if len(c.ID) > 8 {
					suffix = c.ID[:8]
				}
				tempName := containerName + "-old-" + suffix
				if err := cli.ContainerRename(ctx, c.ID, tempName); err != nil {
					log.Printf("[ERROR] Failed to rename container for self-update: %v", err)
					if notifier != nil {
						notifier.SendError(sanitize(groupKey), "Self-update failed: rename error")
					}
					return fmt.Errorf("failed to rename container for self-update: %w", err)
				}
				log.Printf("[INFO] Renamed %s to %s", sanitize(containerName), sanitize(tempName))

				// Create and start new container with original name
				if err := docker.CreateAndStartContainer(ctx, cli, c, containerName); err != nil {
					// Rollback: rename back to original
					log.Printf("[ERROR] Failed to create new container, rolling back: %v", err)
					rbCtx, cancel := docker.RollbackContext(ctx)
					cli.ContainerRename(rbCtx, c.ID, containerName)
					cancel()
					if notifier != nil {
						notifier.SendError(sanitize(groupKey), "Self-update failed: could not start new container")
					}
					return fmt.Errorf("failed to create new container for self-update: %w", err)
				}

				log.Printf("[INFO] New container started, stopping old container")
				if notifier != nil {
					notifier.SendUpdate(sanitize(groupKey), sanitize(imageName), oldID, latestID)
				}

				// Explicitly stop the old (renamed) container via the Docker API so that
				// restart: unless-stopped does not restart it. A bare os.Exit(0) is treated
				// by Docker as an unexpected exit, which triggers the restart policy.
				// ContainerStop marks the container as explicitly stopped, preventing that.
				// With timeout=0 Docker sends SIGKILL immediately; our process is killed
				// before reaching os.Exit below. Uses a detached context so the stop
				// still goes through if the update's context has expired.
				stopCtx, cancel := docker.RollbackContext(ctx)
				stopTimeout := 0
				if err := cli.ContainerStop(stopCtx, c.ID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
					log.Printf("[WARN] Failed to stop old container, falling back to os.Exit: %v", err)
				}
				cancel()
				os.Exit(0)
			}

			log.Printf("[INFO] Recreating container %s", sanitize(containerName))
			newID, err := docker.RecreateContainer(ctx, cli, c, recreated)
			if err != nil {
				log.Printf("[ERROR] Failed to recreate container %s: %v", sanitize(containerName), err)
				if notifier != nil {
					notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to recreate container %s: %v", sanitize(containerName), err))
				}
				return fmt.Errorf("failed to recreate container %s: %w", containerName, err)
			}
			// Track the old->new ID mapping for resolving network_mode references
			recreated[c.ID] = newID
			log.Printf("[INFO] Successfully recreated %s", sanitize(containerName))

			// Recreate containers that share this container's network namespace.
			// Their network_mode still points to the old (now dead) container ID,
			// so they've already lost connectivity — recreating them is recovery, not risk.
			deps, depErr := docker.FindNetworkDependents(ctx, cli, c.ID)
			if depErr != nil {
				log.Printf("[WARN] Failed to find network dependents of %s: %v", sanitize(containerName), depErr)
			}
			for _, dep := range deps {
				depName := strings.TrimPrefix(dep.Name, "/")
				if depName == "" {
					depName = docker.ShortID(dep.ID)
				}
				log.Printf("[INFO] Recreating network-dependent container %s", sanitize(depName))
				depNewID, depRecErr := docker.RecreateContainer(ctx, cli, dep, recreated)
				if depRecErr != nil {
					log.Printf("[WARN] Failed to recreate network-dependent container %s: %v", sanitize(depName), depRecErr)
					continue
				}
				recreated[dep.ID] = depNewID
				log.Printf("[INFO] Successfully recreated network-dependent %s", sanitize(depName))
			}
		}

		// Send success notification after all containers in group are recreated
		if notifier != nil {
			notifier.SendUpdate(sanitize(groupKey), sanitize(imageName), oldID, latestID)
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

// sanitize replaces control characters (newlines, ANSI escapes, etc.) in strings
// derived from external sources — container names, image names, compose labels —
// before they are written to logs or sent as notifications. This prevents log
// injection via crafted container names.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '·'
		}
		return r
	}, s)
}

// isSelf checks if the given container has the io.repull.app label,
// which is baked into the repull Docker image. This is the same approach
// Watchtower uses (com.centurylinklabs.watchtower label).
func isSelf(c container.InspectResponse) bool {
	if c.Config == nil || c.Config.Labels == nil {
		return false
	}
	return c.Config.Labels["io.repull.app"] == "true"
}
