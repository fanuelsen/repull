package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// RecreatedContainers tracks containers that were recreated during an update cycle.
// Maps old container ID to new container ID.
type RecreatedContainers map[string]string

// shortID returns the first 12 characters of a container ID, or the full ID if shorter.
func shortID(id string) string {
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
			if strings.HasPrefix(oldID, ref) || strings.HasPrefix(ref, shortID(oldID)) {
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

	// Get full container details
	var detailed []container.InspectResponse
	for _, c := range containers {
		inspect, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect container %s: %w", c.ID, err)
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
}

// buildContainerConfigs extracts the container, host, and network configs from
// an existing container's inspect response. This is used by both RecreateContainer
// and CreateAndStartContainer to avoid duplicating the config-building logic.
func buildContainerConfigs(ctx context.Context, cli *client.Client, old container.InspectResponse, recreated RecreatedContainers) containerConfigs {
	// Build port bindings
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	if old.HostConfig != nil && old.HostConfig.PortBindings != nil {
		for port, bindings := range old.HostConfig.PortBindings {
			portBindings[port] = bindings
			exposedPorts[port] = struct{}{}
		}
	}

	// Determine if we can set hostname
	// Hostname conflicts with network modes: container:, host, none
	canSetHostname := true
	if old.HostConfig != nil {
		mode := string(old.HostConfig.NetworkMode)
		if len(mode) >= 10 && mode[:10] == "container:" {
			canSetHostname = false
		} else if mode == "host" || mode == "none" {
			canSetHostname = false
		}
	}

	config := &container.Config{
		Image:        old.Config.Image,
		Cmd:          old.Config.Cmd,
		Entrypoint:   old.Config.Entrypoint,
		Env:          old.Config.Env,
		Labels:       old.Config.Labels,
		ExposedPorts: exposedPorts,
		WorkingDir:   old.Config.WorkingDir,
		User:         old.Config.User,
	}

	if canSetHostname {
		config.Hostname = old.Config.Hostname
	}

	// Resolve network mode in case it references a container that was recreated
	networkMode := resolveNetworkMode(ctx, cli, old.HostConfig.NetworkMode, recreated)

	hostConfig := &container.HostConfig{
		Binds:          old.HostConfig.Binds,
		PortBindings:   portBindings,
		RestartPolicy:  old.HostConfig.RestartPolicy,
		NetworkMode:    networkMode,
		CapAdd:         old.HostConfig.CapAdd,
		CapDrop:        old.HostConfig.CapDrop,
		DNS:            old.HostConfig.DNS,
		DNSSearch:      old.HostConfig.DNSSearch,
		ExtraHosts:     old.HostConfig.ExtraHosts,
		Privileged:     old.HostConfig.Privileged,
		SecurityOpt:    old.HostConfig.SecurityOpt,
		Resources:      old.HostConfig.Resources,
		Tmpfs:          old.HostConfig.Tmpfs,
		Sysctls:        old.HostConfig.Sysctls,
		ShmSize:        old.HostConfig.ShmSize,
		PidMode:        old.HostConfig.PidMode,
		IpcMode:        old.HostConfig.IpcMode,
		UTSMode:        old.HostConfig.UTSMode,
		GroupAdd:       old.HostConfig.GroupAdd,
		ReadonlyRootfs: old.HostConfig.ReadonlyRootfs,
		LogConfig:      old.HostConfig.LogConfig,
	}

	// Network settings - Docker only allows one network at creation time.
	// We connect to the first network during creation, then add others after.
	// Sort network names for deterministic ordering across runs.
	netConfig := &network.NetworkingConfig{}
	var additional []string
	if old.NetworkSettings != nil && len(old.NetworkSettings.Networks) > 0 {
		names := make([]string, 0, len(old.NetworkSettings.Networks))
		for name := range old.NetworkSettings.Networks {
			names = append(names, name)
		}
		sort.Strings(names)

		netConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			names[0]: old.NetworkSettings.Networks[names[0]],
		}
		additional = names[1:]
	}

	return containerConfigs{
		config:             config,
		hostConfig:         hostConfig,
		networkConfig:      netConfig,
		additionalNetworks: additional,
	}
}

// createAndConnectNetworks creates a container, connects it to additional networks,
// and starts it. On any failure the partially-created container is removed.
// Returns the new container ID.
func createAndConnectNetworks(ctx context.Context, cli *client.Client, old container.InspectResponse, cc containerConfigs, name string) (string, error) {
	resp, err := cli.ContainerCreate(ctx, cc.config, cc.hostConfig, cc.networkConfig, nil, name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	// Connect to additional networks before starting
	for _, netName := range cc.additionalNetworks {
		endpointConfig := old.NetworkSettings.Networks[netName]
		if err := cli.NetworkConnect(ctx, netName, resp.ID, endpointConfig); err != nil {
			cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			return "", fmt.Errorf("failed to connect container to network %s: %w", netName, err)
		}
	}

	// Start the new container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
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

	// Stop the old container
	timeout := 10
	stopOptions := container.StopOptions{
		Timeout: &timeout,
	}
	if err := cli.ContainerStop(ctx, oldID, stopOptions); err != nil {
		return "", fmt.Errorf("failed to stop container %s: %w", oldID, err)
	}

	// Rename old container to free up the name for the new one.
	// If creation fails we can rename it back and restart as rollback.
	tempName := oldName + "-old-" + shortID(oldID)
	if err := cli.ContainerRename(ctx, oldID, tempName); err != nil {
		// Rename failed — try to restart the old container and bail
		cli.ContainerStart(ctx, oldID, container.StartOptions{})
		return "", fmt.Errorf("failed to rename container %s: %w", oldID, err)
	}

	cc := buildContainerConfigs(ctx, cli, oldContainer, recreated)

	newID, err := createAndConnectNetworks(ctx, cli, oldContainer, cc, oldName)
	if err != nil {
		// Rollback: rename old container back and restart it
		cli.ContainerRename(ctx, oldID, oldName)
		cli.ContainerStart(ctx, oldID, container.StartOptions{})
		return "", err
	}

	// New container is running — clean up old one (best-effort)
	cli.ContainerRemove(ctx, oldID, container.RemoveOptions{})

	return newID, nil
}

// CreateAndStartContainer creates and starts a new container based on an existing container's config.
// Used for self-update where we can't stop the old container before creating the new one.
// The newName parameter specifies the name for the new container.
func CreateAndStartContainer(ctx context.Context, cli *client.Client, oldContainer container.InspectResponse, newName string) error {
	cc := buildContainerConfigs(ctx, cli, oldContainer, nil)

	_, err := createAndConnectNetworks(ctx, cli, oldContainer, cc, newName)
	return err
}
