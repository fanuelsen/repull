package docker

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// RollbackContext returns a context for rollback and cleanup operations.
// It keeps ctx's values but detaches from its cancellation, with a fresh
// 30-second timeout. Rollbacks most often run right after the update's
// context has expired (e.g. a slow pull ate the deadline) — reusing that
// dead context would make the rollback fail exactly when it matters most,
// leaving the old container stopped and renamed.
func RollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

// RecreatedContainers tracks containers that were recreated during an update cycle.
// Maps old container ID to new container ID.
type RecreatedContainers map[string]string

// ShortID returns the first 12 characters of a container ID, or the full ID if shorter.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// resolveNetworkMode checks if the network mode references another container
// and resolves it to the current container ID. This handles the case where
// Docker Compose translates "network_mode: service:name" to "container:<id>"
// and that referenced container has since been recreated with a new ID.
//
// The recreated parameter contains a mapping of old container IDs to new IDs
// for containers that were recreated in the current update cycle.
func resolveNetworkMode(ctx context.Context, cli *client.Client, mode container.NetworkMode, recreated RecreatedContainers) container.NetworkMode {
	modeStr := string(mode)
	if !strings.HasPrefix(modeStr, "container:") {
		return mode
	}

	// Extract the container reference (could be ID or name)
	ref := strings.TrimPrefix(modeStr, "container:")

	// First, check if this references a container we just recreated
	if recreated != nil {
		if newID, ok := recreated[ref]; ok {
			return container.NetworkMode("container:" + newID)
		}
		// Also check partial ID matches (Docker often uses short IDs)
		for oldID, newID := range recreated {
			if strings.HasPrefix(oldID, ref) || strings.HasPrefix(ref, ShortID(oldID)) {
				return container.NetworkMode("container:" + newID)
			}
		}
	}

	// Try to inspect the container by the reference
	inspect, err := cli.ContainerInspect(ctx, ref)
	if err == nil {
		// Container exists, use its current ID
		return container.NetworkMode("container:" + inspect.ID)
	}

	// Container not found by that reference - it might be a stale ID
	// Try to find a container by searching all containers for a matching name
	// Docker names have a leading slash, so we check both with and without
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		// Can't list containers, return original mode and let it fail later
		return mode
	}

	for _, c := range containers {
		for _, name := range c.Names {
			// Docker container names have a leading slash
			cleanName := strings.TrimPrefix(name, "/")
			if cleanName == ref || name == ref {
				return container.NetworkMode("container:" + c.ID)
			}
		}
	}

	// Couldn't resolve, return original mode
	return mode
}

// FindNetworkDependents returns all running containers whose network_mode
// references the given container ID (i.e. network_mode: container:<id>).
// This is used to find containers that will lose connectivity when the
// referenced container is recreated.
func FindNetworkDependents(ctx context.Context, cli *client.Client, containerID string) ([]container.InspectResponse, error) {
	filter := filters.NewArgs()
	filter.Add("status", "running")

	containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	short := ShortID(containerID)
	var dependents []container.InspectResponse
	for _, c := range containers {
		inspect, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			continue
		}
		if inspect.HostConfig == nil {
			continue
		}
		mode := string(inspect.HostConfig.NetworkMode)
		if !strings.HasPrefix(mode, "container:") {
			continue
		}
		ref := strings.TrimPrefix(mode, "container:")
		if ref == containerID || strings.HasPrefix(containerID, ref) || strings.HasPrefix(ref, short) {
			dependents = append(dependents, inspect)
		}
	}
	return dependents, nil
}

// CleanupSelfUpdateLeftovers removes containers left behind by previous
// self-updates. Self-update renames the old container and stops it, but the
// old process is killed before it can remove itself, so the new container
// does it on the next startup.
//
// A leftover is identified by the rename pattern self-update produces
// ("<name>-old-<its own short ID>"), not by the io.repull.app label alone.
// Docker merges image labels into container labels, so trusting the label
// would let any third-party image carrying io.repull.app=true get unrelated
// containers force-removed here — including, if it sorted newer, repull
// itself. The rename suffix embeds the container's own ID, which an image
// label cannot forge. The label filter remains as a cheap server-side
// pre-filter only.
//
// Returns the names of containers that were removed.
func CleanupSelfUpdateLeftovers(ctx context.Context, cli *client.Client) ([]string, error) {
	filter := filters.NewArgs()
	filter.Add("label", "io.repull.app=true")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list repull containers: %w", err)
	}

	// Inside a container the hostname defaults to the short container ID;
	// used below to make sure this process's own container is never removed.
	hostname, _ := os.Hostname()

	var removed []string
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if hostname != "" && (strings.HasPrefix(c.ID, hostname) || name == hostname) {
			continue
		}
		if !isSelfUpdateLeftover(name, c.ID) {
			continue
		}
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			continue
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// isSelfUpdateLeftover reports whether a container is the remnant of a
// previous self-update: its name ends in the "-old-<short ID>" suffix that
// updateRepullInstance appends on rename, where the short ID is the
// container's own. A name that is only the suffix (empty original name)
// does not count.
func isSelfUpdateLeftover(name, id string) bool {
	suffix := "-old-" + ShortID(id)
	return len(name) > len(suffix) && strings.HasSuffix(name, suffix)
}

// ListRunningContainers returns all currently running containers.
func ListRunningContainers(ctx context.Context, cli *client.Client) ([]container.InspectResponse, error) {
	filter := filters.NewArgs()
	filter.Add("status", "running")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filter,
	})
	if err != nil {
		return nil, err
	}

	// Get full container details. A container can exit between the list and
	// the inspect calls — skip it instead of failing the whole update cycle.
	var detailed []container.InspectResponse
	for _, c := range containers {
		inspect, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			log.Printf("[WARN] Skipping container %s: inspect failed: %v", ShortID(c.ID), err)
			continue
		}
		detailed = append(detailed, inspect)
	}

	return detailed, nil
}

// containerConfigs holds the configs needed to create a new container.
type containerConfigs struct {
	config        *container.Config
	hostConfig    *container.HostConfig
	networkConfig *network.NetworkingConfig
	// additionalNetworks are connected after creation (Docker only allows one at create time).
	additionalNetworks []string
	// endpoints holds the sanitized endpoint settings for every network, keyed by network name.
	endpoints map[string]*network.EndpointSettings
}

// sanitizeEndpoint copies the parts of an endpoint's settings that represent
// user configuration (static IPs, aliases, links, driver options) and drops
// runtime state (endpoint ID, assigned IP and MAC addresses, DNS names).
// Reusing runtime fields on a new container would pin stale values — for
// example the old container's auto-assigned MAC address, or a DNS alias
// pointing at the old container's short ID.
func sanitizeEndpoint(old *network.EndpointSettings, oldContainerID string) *network.EndpointSettings {
	if old == nil {
		return nil
	}

	ep := &network.EndpointSettings{
		IPAMConfig: old.IPAMConfig,
		Links:      old.Links,
		DriverOpts: old.DriverOpts,
		GwPriority: old.GwPriority,
	}

	// Docker and Compose add the container's short ID as a network alias.
	// Keep user-defined aliases but drop the old container's ID alias.
	oldShort := ShortID(oldContainerID)
	for _, alias := range old.Aliases {
		if alias != oldShort {
			ep.Aliases = append(ep.Aliases, alias)
		}
	}

	return ep
}

// recreatePortConfig computes the exposed ports, published-port bindings, and
// publish-all flag for a recreated container.
//
// Normally it starts from the image/container's own exposed ports (covering
// compose "expose:" entries) and adds every published port. But a container
// that joins another container's network namespace (network_mode: container:,
// which compose's "service:<name>" resolves to) shares that container's ports
// and may declare none of its own — the daemon rejects such a create with
// "conflicting options: port exposing/publishing and the container type network
// mode". Because the inspected container still reports the image's EXPOSE in
// Config.ExposedPorts, those must be dropped explicitly; everything is returned
// empty for that mode.
func recreatePortConfig(cfg *container.Config, host *container.HostConfig) (nat.PortSet, nat.PortMap, bool) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	if strings.HasPrefix(string(host.NetworkMode), "container:") {
		return exposed, bindings, false
	}
	for port := range cfg.ExposedPorts {
		exposed[port] = struct{}{}
	}
	for port, b := range host.PortBindings {
		bindings[port] = b
		exposed[port] = struct{}{}
	}
	return exposed, bindings, host.PublishAllPorts
}

// buildContainerConfigs extracts the container, host, and network configs from
// an existing container's inspect response. This is used by both RecreateContainer
// and CreateAndStartContainer to avoid duplicating the config-building logic.
func buildContainerConfigs(ctx context.Context, cli *client.Client, old container.InspectResponse, recreated RecreatedContainers) containerConfigs {
	// Inspect responses always include Config and HostConfig in practice;
	// guard once here so a partial response can't panic the update.
	oldConfig := old.Config
	if oldConfig == nil {
		oldConfig = &container.Config{}
	}
	oldHost := old.HostConfig
	if oldHost == nil {
		oldHost = &container.HostConfig{}
	}

	// Determine if we can set hostname
	// Hostname conflicts with network modes: container:, host, none
	mode := string(oldHost.NetworkMode)
	canSetHostname := !strings.HasPrefix(mode, "container:") && mode != "host" && mode != "none"

	exposedPorts, portBindings, publishAllPorts := recreatePortConfig(oldConfig, oldHost)

	config := &container.Config{
		Image:        oldConfig.Image,
		Cmd:          oldConfig.Cmd,
		Entrypoint:   oldConfig.Entrypoint,
		Env:          oldConfig.Env,
		Labels:       oldConfig.Labels,
		ExposedPorts: exposedPorts,
		WorkingDir:   oldConfig.WorkingDir,
		User:         oldConfig.User,
		Healthcheck:  oldConfig.Healthcheck,
		StopSignal:   oldConfig.StopSignal,
		StopTimeout:  oldConfig.StopTimeout,
		Volumes:      oldConfig.Volumes,
		Tty:          oldConfig.Tty,
		OpenStdin:    oldConfig.OpenStdin,
		StdinOnce:    oldConfig.StdinOnce,
		AttachStdin:  oldConfig.AttachStdin,
		AttachStdout: oldConfig.AttachStdout,
		AttachStderr: oldConfig.AttachStderr,
		Domainname:   oldConfig.Domainname,
	}

	if canSetHostname {
		config.Hostname = oldConfig.Hostname
	}

	// Resolve network mode in case it references a container that was recreated
	networkMode := resolveNetworkMode(ctx, cli, oldHost.NetworkMode, recreated)

	hostConfig := &container.HostConfig{
		Binds:           oldHost.Binds,
		Mounts:          oldHost.Mounts,
		VolumesFrom:     oldHost.VolumesFrom,
		VolumeDriver:    oldHost.VolumeDriver,
		PortBindings:    portBindings,
		PublishAllPorts: publishAllPorts,
		RestartPolicy:   oldHost.RestartPolicy,
		AutoRemove:      oldHost.AutoRemove,
		NetworkMode:     networkMode,
		Links:           oldHost.Links,
		CapAdd:          oldHost.CapAdd,
		CapDrop:         oldHost.CapDrop,
		DNS:             oldHost.DNS,
		DNSOptions:      oldHost.DNSOptions,
		DNSSearch:       oldHost.DNSSearch,
		ExtraHosts:      oldHost.ExtraHosts,
		Privileged:      oldHost.Privileged,
		SecurityOpt:     oldHost.SecurityOpt,
		MaskedPaths:     oldHost.MaskedPaths,
		ReadonlyPaths:   oldHost.ReadonlyPaths,
		Resources:       oldHost.Resources,
		OomScoreAdj:     oldHost.OomScoreAdj,
		Tmpfs:           oldHost.Tmpfs,
		StorageOpt:      oldHost.StorageOpt,
		Sysctls:         oldHost.Sysctls,
		ShmSize:         oldHost.ShmSize,
		PidMode:         oldHost.PidMode,
		IpcMode:         oldHost.IpcMode,
		UTSMode:         oldHost.UTSMode,
		UsernsMode:      oldHost.UsernsMode,
		Cgroup:          oldHost.Cgroup,
		CgroupnsMode:    oldHost.CgroupnsMode,
		GroupAdd:        oldHost.GroupAdd,
		ReadonlyRootfs:  oldHost.ReadonlyRootfs,
		Runtime:         oldHost.Runtime,
		Init:            oldHost.Init,
		Isolation:       oldHost.Isolation,
		LogConfig:       oldHost.LogConfig,
		Annotations:     oldHost.Annotations,
	}

	// Network settings - Docker only allows one network at creation time.
	// We connect to the first network during creation, then add others after.
	// Sort network names for deterministic ordering across runs.
	netConfig := &network.NetworkingConfig{}
	var additional []string
	endpoints := make(map[string]*network.EndpointSettings)
	if old.NetworkSettings != nil && len(old.NetworkSettings.Networks) > 0 {
		names := make([]string, 0, len(old.NetworkSettings.Networks))
		for name, ep := range old.NetworkSettings.Networks {
			names = append(names, name)
			endpoints[name] = sanitizeEndpoint(ep, old.ID)
		}
		sort.Strings(names)

		netConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			names[0]: endpoints[names[0]],
		}
		additional = names[1:]
	}

	return containerConfigs{
		config:             config,
		hostConfig:         hostConfig,
		networkConfig:      netConfig,
		additionalNetworks: additional,
		endpoints:          endpoints,
	}
}

// createAndConnectNetworks creates a container, connects it to additional networks,
// and starts it. On any failure the partially-created container is removed.
// Returns the new container ID.
func createAndConnectNetworks(ctx context.Context, cli *client.Client, cc containerConfigs, name string) (string, error) {
	resp, err := cli.ContainerCreate(ctx, cc.config, cc.hostConfig, cc.networkConfig, nil, name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	// Connect to additional networks before starting
	for _, netName := range cc.additionalNetworks {
		endpointConfig := cc.endpoints[netName]
		if err := cli.NetworkConnect(ctx, netName, resp.ID, endpointConfig); err != nil {
			rbCtx, cancel := RollbackContext(ctx)
			defer cancel()
			cli.ContainerRemove(rbCtx, resp.ID, container.RemoveOptions{Force: true})
			return "", fmt.Errorf("failed to connect container to network %s: %w", netName, err)
		}
	}

	// Start the new container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		rbCtx, cancel := RollbackContext(ctx)
		defer cancel()
		cli.ContainerRemove(rbCtx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("failed to start container %s: %w", resp.ID, err)
	}

	return resp.ID, nil
}

// RecreateContainer stops and recreates a container with the same configuration
// but with a potentially updated image. Returns the new container ID.
//
// The image must already be pulled by the caller (UpdateGroups handles this).
//
// Uses a rename-based approach to avoid data loss: the old container is stopped
// and renamed (not removed) before creating the new one. If creation fails, the
// old container is renamed back and restarted as a rollback.
//
// The recreated parameter contains a mapping of old container IDs to new IDs
// for containers that were recreated earlier in the current update cycle.
// This is used to resolve stale network_mode references.
func RecreateContainer(ctx context.Context, cli *client.Client, oldContainer container.InspectResponse, recreated RecreatedContainers) (string, error) {
	oldID := oldContainer.ID
	oldName := oldContainer.Name

	// Stop the old container. A nil timeout lets Docker use the container's
	// own StopTimeout (compose stop_grace_period) or the daemon default of
	// 10s — a hardcoded value here would cut short containers that declare
	// they need longer to shut down cleanly (e.g. databases).
	if err := cli.ContainerStop(ctx, oldID, container.StopOptions{}); err != nil {
		return "", fmt.Errorf("failed to stop container %s: %w", oldID, err)
	}

	// Rename old container to free up the name for the new one.
	// If creation fails we can rename it back and restart as rollback.
	tempName := oldName + "-old-" + ShortID(oldID)
	if err := cli.ContainerRename(ctx, oldID, tempName); err != nil {
		// Rename failed — try to restart the old container and bail
		rbCtx, cancel := RollbackContext(ctx)
		defer cancel()
		cli.ContainerStart(rbCtx, oldID, container.StartOptions{})
		return "", fmt.Errorf("failed to rename container %s: %w", oldID, err)
	}

	cc := buildContainerConfigs(ctx, cli, oldContainer, recreated)

	newID, err := createAndConnectNetworks(ctx, cli, cc, oldName)
	if err != nil {
		// Rollback: rename old container back and restart it
		rbCtx, cancel := RollbackContext(ctx)
		defer cancel()
		cli.ContainerRename(rbCtx, oldID, oldName)
		cli.ContainerStart(rbCtx, oldID, container.StartOptions{})
		return "", err
	}

	// New container is running — clean up old one (best-effort). Uses the
	// rollback context so the removal still happens if ctx has expired.
	rmCtx, cancel := RollbackContext(ctx)
	defer cancel()
	cli.ContainerRemove(rmCtx, oldID, container.RemoveOptions{})

	return newID, nil
}

// CreateAndStartContainer creates and starts a new container based on an existing container's config.
// Used for self-update where we can't stop the old container before creating the new one.
// The newName parameter specifies the name for the new container.
func CreateAndStartContainer(ctx context.Context, cli *client.Client, oldContainer container.InspectResponse, newName string) error {
	cc := buildContainerConfigs(ctx, cli, oldContainer, nil)

	_, err := createAndConnectNetworks(ctx, cli, cc, newName)
	return err
}
