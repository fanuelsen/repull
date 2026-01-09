package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

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

	hostConfig := &container.HostConfig{
		Binds:          oldContainer.HostConfig.Binds,
		PortBindings:   portBindings,
		RestartPolicy:  oldContainer.HostConfig.RestartPolicy,
		NetworkMode:    oldContainer.HostConfig.NetworkMode,
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
