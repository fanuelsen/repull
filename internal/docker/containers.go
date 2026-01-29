package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// resolveNetworkMode checks if the network mode references another container
// and resolves it to the current container ID. This handles the case where
// Docker Compose translates "network_mode: service:name" to "container:<id>"
// and that referenced container has since been recreated with a new ID.
func resolveNetworkMode(ctx context.Context, cli *client.Client, mode container.NetworkMode) container.NetworkMode {
	modeStr := string(mode)
	if !strings.HasPrefix(modeStr, "container:") {
		return mode
	}

	// Extract the container reference (could be ID or name)
	ref := strings.TrimPrefix(modeStr, "container:")

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

// RecreateContainer stops, removes, and recreates a container with the same configuration
// but with a potentially updated image.
func RecreateContainer(ctx context.Context, cli *client.Client, oldContainer container.InspectResponse) error {
	oldID := oldContainer.ID
	oldName := oldContainer.Name

	// Pull the latest image first
	if err := PullImage(ctx, cli, oldContainer.Config.Image); err != nil {
		return fmt.Errorf("failed to pull image %s: %w", oldContainer.Config.Image, err)
	}

	// Stop the old container
	timeout := 10
	stopOptions := container.StopOptions{
		Timeout: &timeout,
	}
	if err := cli.ContainerStop(ctx, oldID, stopOptions); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", oldID, err)
	}

	// Remove the old container
	if err := cli.ContainerRemove(ctx, oldID, container.RemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", oldID, err)
	}

	// Build port bindings
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	if oldContainer.HostConfig != nil && oldContainer.HostConfig.PortBindings != nil {
		for port, bindings := range oldContainer.HostConfig.PortBindings {
			portBindings[port] = bindings
			exposedPorts[port] = struct{}{}
		}
	}

	// Determine if we can set hostname
	// Hostname conflicts with network modes: container:, host, none
	canSetHostname := true
	if oldContainer.HostConfig != nil {
		mode := string(oldContainer.HostConfig.NetworkMode)
		// Check if using container:, host, or none network mode
		if len(mode) >= 10 && mode[:10] == "container:" {
			canSetHostname = false
		} else if mode == "host" || mode == "none" {
			canSetHostname = false
		}
	}

	// Create new container with same config
	config := &container.Config{
		Image:        oldContainer.Config.Image,
		Cmd:          oldContainer.Config.Cmd,
		Entrypoint:   oldContainer.Config.Entrypoint,
		Env:          oldContainer.Config.Env,
		Labels:       oldContainer.Config.Labels,
		ExposedPorts: exposedPorts,
		WorkingDir:   oldContainer.Config.WorkingDir,
		User:         oldContainer.Config.User,
	}

	// Only set hostname if network mode allows it
	if canSetHostname {
		config.Hostname = oldContainer.Config.Hostname
	}

	// Resolve network mode in case it references a container that was recreated
	networkMode := resolveNetworkMode(ctx, cli, oldContainer.HostConfig.NetworkMode)

	hostConfig := &container.HostConfig{
		Binds:          oldContainer.HostConfig.Binds,
		PortBindings:   portBindings,
		RestartPolicy:  oldContainer.HostConfig.RestartPolicy,
		NetworkMode:    networkMode,
		CapAdd:         oldContainer.HostConfig.CapAdd,
		CapDrop:        oldContainer.HostConfig.CapDrop,
		DNS:            oldContainer.HostConfig.DNS,
		DNSSearch:      oldContainer.HostConfig.DNSSearch,
		ExtraHosts:     oldContainer.HostConfig.ExtraHosts,
		Privileged:     oldContainer.HostConfig.Privileged,
		SecurityOpt:    oldContainer.HostConfig.SecurityOpt,
		Resources:      oldContainer.HostConfig.Resources,
		Tmpfs:          oldContainer.HostConfig.Tmpfs,
		Sysctls:        oldContainer.HostConfig.Sysctls,
		ShmSize:        oldContainer.HostConfig.ShmSize,
		PidMode:        oldContainer.HostConfig.PidMode,
		IpcMode:        oldContainer.HostConfig.IpcMode,
		UTSMode:        oldContainer.HostConfig.UTSMode,
		GroupAdd:       oldContainer.HostConfig.GroupAdd,
		ReadonlyRootfs: oldContainer.HostConfig.ReadonlyRootfs,
		LogConfig:      oldContainer.HostConfig.LogConfig,
	}

	// Network settings - Docker only allows one network at creation time.
	// We connect to the first network during creation, then add others after.
	networkConfig := &network.NetworkingConfig{}
	var additionalNetworks []string
	if oldContainer.NetworkSettings != nil && len(oldContainer.NetworkSettings.Networks) > 0 {
		first := true
		for netName, netConfig := range oldContainer.NetworkSettings.Networks {
			if first {
				networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
					netName: netConfig,
				}
				first = false
			} else {
				additionalNetworks = append(additionalNetworks, netName)
			}
		}
	}

	// Create new container
	resp, err := cli.ContainerCreate(ctx, config, hostConfig, networkConfig, nil, oldName)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Connect to additional networks before starting
	for _, netName := range additionalNetworks {
		endpointConfig := oldContainer.NetworkSettings.Networks[netName]
		if err := cli.NetworkConnect(ctx, netName, resp.ID, endpointConfig); err != nil {
			return fmt.Errorf("failed to connect container to network %s: %w", netName, err)
		}
	}

	// Start the new container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", resp.ID, err)
	}

	return nil
}

// CreateAndStartContainer creates and starts a new container based on an existing container's config.
// Used for self-update where we can't stop the old container before creating the new one.
// The newName parameter specifies the name for the new container.
func CreateAndStartContainer(ctx context.Context, cli *client.Client, oldContainer container.InspectResponse, newName string) error {
	// Build port bindings
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	if oldContainer.HostConfig != nil && oldContainer.HostConfig.PortBindings != nil {
		for port, bindings := range oldContainer.HostConfig.PortBindings {
			portBindings[port] = bindings
			exposedPorts[port] = struct{}{}
		}
	}

	// Determine if we can set hostname
	canSetHostname := true
	if oldContainer.HostConfig != nil {
		mode := string(oldContainer.HostConfig.NetworkMode)
		if len(mode) >= 10 && mode[:10] == "container:" {
			canSetHostname = false
		} else if mode == "host" || mode == "none" {
			canSetHostname = false
		}
	}

	// Create new container config
	config := &container.Config{
		Image:        oldContainer.Config.Image,
		Cmd:          oldContainer.Config.Cmd,
		Entrypoint:   oldContainer.Config.Entrypoint,
		Env:          oldContainer.Config.Env,
		Labels:       oldContainer.Config.Labels,
		ExposedPorts: exposedPorts,
		WorkingDir:   oldContainer.Config.WorkingDir,
		User:         oldContainer.Config.User,
	}

	if canSetHostname {
		config.Hostname = oldContainer.Config.Hostname
	}

	// Resolve network mode in case it references a container that was recreated
	networkMode := resolveNetworkMode(ctx, cli, oldContainer.HostConfig.NetworkMode)

	hostConfig := &container.HostConfig{
		Binds:          oldContainer.HostConfig.Binds,
		PortBindings:   portBindings,
		RestartPolicy:  oldContainer.HostConfig.RestartPolicy,
		NetworkMode:    networkMode,
		CapAdd:         oldContainer.HostConfig.CapAdd,
		CapDrop:        oldContainer.HostConfig.CapDrop,
		DNS:            oldContainer.HostConfig.DNS,
		DNSSearch:      oldContainer.HostConfig.DNSSearch,
		ExtraHosts:     oldContainer.HostConfig.ExtraHosts,
		Privileged:     oldContainer.HostConfig.Privileged,
		SecurityOpt:    oldContainer.HostConfig.SecurityOpt,
		Resources:      oldContainer.HostConfig.Resources,
		Tmpfs:          oldContainer.HostConfig.Tmpfs,
		Sysctls:        oldContainer.HostConfig.Sysctls,
		ShmSize:        oldContainer.HostConfig.ShmSize,
		PidMode:        oldContainer.HostConfig.PidMode,
		IpcMode:        oldContainer.HostConfig.IpcMode,
		UTSMode:        oldContainer.HostConfig.UTSMode,
		GroupAdd:       oldContainer.HostConfig.GroupAdd,
		ReadonlyRootfs: oldContainer.HostConfig.ReadonlyRootfs,
		LogConfig:      oldContainer.HostConfig.LogConfig,
	}

	// Network settings
	networkConfig := &network.NetworkingConfig{}
	var additionalNetworks []string
	if oldContainer.NetworkSettings != nil && len(oldContainer.NetworkSettings.Networks) > 0 {
		first := true
		for netName, netConfig := range oldContainer.NetworkSettings.Networks {
			if first {
				networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
					netName: netConfig,
				}
				first = false
			} else {
				additionalNetworks = append(additionalNetworks, netName)
			}
		}
	}

	// Create new container with the specified name
	resp, err := cli.ContainerCreate(ctx, config, hostConfig, networkConfig, nil, newName)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Connect to additional networks before starting
	for _, netName := range additionalNetworks {
		endpointConfig := oldContainer.NetworkSettings.Networks[netName]
		if err := cli.NetworkConnect(ctx, netName, resp.ID, endpointConfig); err != nil {
			return fmt.Errorf("failed to connect container to network %s: %w", netName, err)
		}
	}

	// Start the new container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", resp.ID, err)
	}

	return nil
}
