package docker

import (
	"context"
	"time"

	"github.com/docker/docker/client"
)

// NewClient creates a new Docker API client using environment variables.
// Respects DOCKER_HOST for remote Docker daemons.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	// Verify connection with a timeout to avoid blocking indefinitely
	// on an unresponsive Docker daemon.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	if err != nil {
		return nil, err
	}

	return cli, nil
}
