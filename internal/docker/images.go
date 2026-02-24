package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// PullImage pulls the latest version of an image from the registry.
func PullImage(ctx context.Context, cli *client.Client, imageName string) error {
	reader, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()

	// Consume the output to ensure pull completes
	_, err = io.Copy(io.Discard, reader)
	return err
}

// GetImageDigest returns the digest (sha256) of an image.
func GetImageDigest(ctx context.Context, cli *client.Client, imageName string) (string, error) {
	inspect, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return "", err
	}

	// Use RepoDigests if available (more reliable for registry images)
	if len(inspect.RepoDigests) > 0 {
		return inspect.RepoDigests[0], nil
	}

	// Fallback to image ID
	return inspect.ID, nil
}

// HasDigestChanged compares two image digests and returns true if they differ.
func HasDigestChanged(oldDigest, newDigest string) bool {
	return oldDigest != newDigest
}

