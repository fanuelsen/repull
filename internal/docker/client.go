package docker

import (
	"context"

	"github.com/docker/docker/client"
)

// NewClient creates a new Docker API client using environment variables.
// Respects DOCKER_HOST for remote Docker daemons.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	// Verify connection
	ctx := context.Background()
	_, err = cli.Ping(ctx)
	if err != nil {
		return nil, err
	}

	return cli, nil
}
