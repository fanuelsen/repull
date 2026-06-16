package updater

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/fanuelsen/repull/internal/docker"
	"github.com/fanuelsen/repull/internal/notify"
)

// groupTimeout bounds the work for a single group: pulling the image and
// recreating its containers. Generous enough for large images on slow links.
const groupTimeout = 10 * time.Minute

// UpdateGroups processes each group of containers and updates them if they are
// running an outdated image. It updates one group at a time (sequential, not
// parallel) for safety. Groups are independent: a failure in one group is
// logged and reported, but the remaining groups are still processed. Returns
// the combined errors of all failed groups, or nil if every group succeeded.
// With cleanup enabled, replaced images are removed after a successful update.
func UpdateGroups(ctx context.Context, cli *client.Client, groups map[string][]container.InspectResponse, dryRun bool, cleanup bool, notifier *notify.Notifier) error {
	// Track containers recreated during this update cycle.
	// This is used to resolve stale network_mode references when containers
	// use network_mode: service:X (which Docker stores as container:<id>).
	recreated := make(docker.RecreatedContainers)

	var errs []error
	for groupKey, containers := range groups {
		if len(containers) == 0 {
			continue
		}

		// Each group gets its own deadline so one slow group (big image, slow
		// registry, stalled daemon) cannot eat the time budget of the others.
		groupCtx, cancel := context.WithTimeout(ctx, groupTimeout)
		err := updateGroup(groupCtx, cli, groupKey, containers, dryRun, cleanup, notifier, recreated)
		cancel()
		if err != nil {
			log.Printf("[ERROR] %s: %v — continuing with remaining groups", sanitize(groupKey), err)
			// Sanitize here too: this error is logged by main without further
			// escaping, so a crafted compose project name must not be able to
			// inject log lines through it.
			errs = append(errs, fmt.Errorf("%s: %w", sanitize(groupKey), err))
		}
	}

	return errors.Join(errs...)
}

// updateGroup pulls the group's image and recreates any of its containers that
// are running an outdated image.
func updateGroup(ctx context.Context, cli *client.Client, groupKey string, containers []container.InspectResponse, dryRun bool, cleanup bool, notifier *notify.Notifier, recreated docker.RecreatedContainers) error {
	log.Printf("[INFO] Checking %s (%d container(s))", sanitize(groupKey), len(containers))

	// Get image name from first container (all containers in a group share the same image)
	imageName := containers[0].Config.Image

	// Pull latest image
	log.Printf("[INFO] Pulling image %s", sanitize(imageName))
	if err := docker.PullImage(ctx, cli, imageName); err != nil {
		notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to pull image %s: %v", sanitize(imageName), err))
		return fmt.Errorf("failed to pull image %s: %w", sanitize(imageName), err)
	}

	// Resolve the image ID the tag points to after the pull
	latestID, err := docker.GetImageID(ctx, cli, imageName)
	if err != nil {
		notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to inspect image %s: %v", sanitize(imageName), err))
		return fmt.Errorf("failed to inspect image %s: %w", sanitize(imageName), err)
	}

	// Compare each container's image ID against the latest. Unlike comparing
	// the tag's digest before/after the pull, this detects outdated containers
	// even when the image was already pulled earlier — by a dry run, a manual
	// docker pull, or a cycle that pulled successfully but failed to recreate.
	outdated := filterOutdatedContainers(containers, latestID)
	if len(outdated) == 0 {
		log.Printf("[INFO] Already running latest image, skipping %s", sanitize(groupKey))
		return nil
	}

	oldID := outdated[0].Image
	log.Printf("[INFO] Image updated: %s -> %s", truncateDigest(oldID), truncateDigest(latestID))

	if dryRun {
		log.Printf("[DRY-RUN] Would recreate %s (%d container(s))", sanitize(groupKey), len(outdated))
		return nil
	}

	// Recreate the outdated containers in the group
	log.Printf("[INFO] Recreating %d container(s)", len(outdated))
	for _, c := range outdated {
		containerName := strings.TrimPrefix(c.Name, "/")
		if containerName == "" {
			containerName = docker.ShortID(c.ID)
		}

		// Containers running a repull image need the rename-first flow: such a
		// container may be this very process, which cannot stop itself before
		// the replacement exists. The container already passed the
		// io.repull.enable=true filter, so the user has opted in.
		if isRepullInstance(c) {
			if err := updateRepullInstance(ctx, cli, c, containerName, groupKey, imageName, oldID, latestID, notifier); err != nil {
				return err
			}
			// Another repull instance was updated; this process is unaffected.
			// (A self-update never reaches this point — the process exits.)
			continue
		}

		log.Printf("[INFO] Recreating container %s", sanitize(containerName))
		newID, err := docker.RecreateContainer(ctx, cli, c, recreated)
		if err != nil {
			notifier.SendError(sanitize(groupKey), fmt.Sprintf("Failed to recreate container %s: %v", sanitize(containerName), err))
			return fmt.Errorf("failed to recreate container %s: %w", sanitize(containerName), err)
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
	notifier.SendUpdate(sanitize(groupKey), sanitize(imageName), truncateDigest(oldID), truncateDigest(latestID))

	// Remove the replaced image(s) now that no container in this group uses
	// them. Not forced: if another container still uses an old image, Docker
	// refuses and we just log it. Only reached when every recreation above
	// succeeded — on a partial failure the old image stays available.
	if cleanup {
		oldImages := make(map[string]struct{})
		for _, c := range outdated {
			oldImages[c.Image] = struct{}{}
		}
		for id := range oldImages {
			if err := docker.RemoveImage(ctx, cli, id); err != nil {
				log.Printf("[WARN] Failed to remove old image %s: %v", truncateDigest(id), err)
				continue
			}
			log.Printf("[INFO] Removed old image %s", truncateDigest(id))
		}
	}

	return nil
}

// updateRepullInstance updates a container running a repull image via the
// rename-first flow: rename the old container, start the replacement under the
// original name, then stop the old one. This order is required because the
// container may be this very process, which cannot stop itself before the
// replacement exists.
//
// If the container is this process (self-update), the function never returns:
// the ContainerStop kills us, with os.Exit(0) as a fallback. For any other
// repull instance it returns normally and the caller continues.
func updateRepullInstance(ctx context.Context, cli *client.Client, c container.InspectResponse, containerName, groupKey, imageName, oldID, latestID string, notifier *notify.Notifier) error {
	hostname, _ := os.Hostname()
	self := isSelfContainer(c, hostname)
	if self {
		log.Printf("[INFO] Self-update detected for %s", sanitize(containerName))
	} else {
		log.Printf("[INFO] Updating repull instance %s (not this process)", sanitize(containerName))
	}

	// Rename current container to allow new container to use the name
	tempName := containerName + "-old-" + docker.ShortID(c.ID)
	if err := cli.ContainerRename(ctx, c.ID, tempName); err != nil {
		notifier.SendError(sanitize(groupKey), "Self-update failed: rename error")
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
		notifier.SendError(sanitize(groupKey), "Self-update failed: could not start new container")
		return fmt.Errorf("failed to create new container for self-update: %w", err)
	}

	log.Printf("[INFO] New container started, stopping old container")
	if self {
		// Send before stopping: if the old container is this process,
		// the stop below kills us and the notification at the end of
		// the group never runs. Non-self instances are covered by the
		// group-level notification instead.
		notifier.SendUpdate(sanitize(groupKey), sanitize(imageName), truncateDigest(oldID), truncateDigest(latestID))
	}

	// Explicitly stop the old (renamed) container via the Docker API so that
	// restart: unless-stopped does not restart it. A bare os.Exit(0) is treated
	// by Docker as an unexpected exit, which triggers the restart policy.
	// ContainerStop marks the container as explicitly stopped, preventing that.
	// If the old container is this process, timeout=0 makes Docker SIGKILL us
	// here; execution past this point means it was another instance (or the
	// stop failed). Uses a detached context so the stop still goes through
	// if the update's context has expired.
	stopCtx, cancel := docker.RollbackContext(ctx)
	stopTimeout := 0
	if err := cli.ContainerStop(stopCtx, c.ID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
		log.Printf("[WARN] Failed to stop old container: %v", err)
	}
	cancel()

	if self {
		// The stop above should have killed us; exit as a fallback so
		// the new instance takes over cleanly.
		os.Exit(0)
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

// isRepullInstance checks if the given container has the io.repull.app label,
// which is baked into the repull Docker image. This is the same approach
// Watchtower uses (com.centurylinklabs.watchtower label). It matches any
// repull instance — not necessarily the one running this process; use
// isSelfContainer to check identity.
func isRepullInstance(c container.InspectResponse) bool {
	if c.Config == nil || c.Config.Labels == nil {
		return false
	}
	return c.Config.Labels["io.repull.app"] == "true"
}

// isSelfContainer reports whether the given container is the one this process
// is running in. Inside a container the hostname defaults to the short
// container ID; if the user set a custom hostname, fall back to matching it
// against the container name. When repull runs as a plain host binary neither
// matches, so other repull containers are correctly treated as not-self.
func isSelfContainer(c container.InspectResponse, hostname string) bool {
	if hostname == "" {
		return false
	}
	if strings.HasPrefix(c.ID, hostname) {
		return true
	}
	return strings.TrimPrefix(c.Name, "/") == hostname
}
