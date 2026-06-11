package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// PullImage pulls the latest version of an image from the registry.
// Credentials for private registries are read from Docker's config.json
// (see RegistryAuthFor); public images work without any configuration.
func PullImage(ctx context.Context, cli *client.Client, imageName string) error {
	opts := image.PullOptions{
		RegistryAuth: RegistryAuthFor(imageName),
	}
	reader, err := cli.ImagePull(ctx, imageName, opts)
	if err != nil {
		return err
	}
	defer reader.Close()

	// Consume the output to ensure pull completes
	_, err = io.Copy(io.Discard, reader)
	return err
}

// GetImageID returns the image ID (sha256:...) that the given image name
// currently resolves to. Comparing this against a container's Image field
// (which holds the ID of the image the container was created from) tells us
// whether the container is running the latest local image — regardless of
// who pulled it or when.
func GetImageID(ctx context.Context, cli *client.Client, imageName string) (string, error) {
	inspect, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return "", err
	}
	return inspect.ID, nil
}
