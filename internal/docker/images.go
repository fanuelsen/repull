package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
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

	// The pull must be consumed to completion, and the stream must be decoded
	// rather than discarded: the daemon reports many failures (rejected
	// credentials, layer download errors) as in-stream JSON error messages
	// with no error on the HTTP call itself. Discarding them would make a
	// failed pull look successful — the tag stays on the old image and the
	// cycle reports "already up to date" instead of the real failure.
	dec := json.NewDecoder(reader)
	for {
		var msg jsonmessage.JSONMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("reading pull progress: %w", err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull failed: %s", msg.Error.Message)
		}
	}
}

// GetImageID returns the image ID (sha256:...) that the given image name
// currently resolves to. Comparing this against a container's Image field
// (which holds the ID of the image the container was created from) tells us
// whether the container is running the latest local image — regardless of
// who pulled it or when.
func GetImageID(ctx context.Context, cli *client.Client, imageName string) (string, error) {
	inspect, err := cli.ImageInspect(ctx, imageName)
	if err != nil {
		return "", err
	}
	return inspect.ID, nil
}

// GetImageConfigLabels returns the labels the image itself defines (its
// build-time LABELs), as opposed to labels set on a container at creation.
// Docker merges the former into the latter, so this is the only way to tell
// where a container's label came from.
func GetImageConfigLabels(ctx context.Context, cli *client.Client, imageRef string) (map[string]string, error) {
	inspect, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		return nil, err
	}
	if inspect.Config == nil {
		return nil, nil
	}
	return inspect.Config.Labels, nil
}

// RemoveImage removes an image by ID. Used to clean up replaced images after
// a successful update when --cleanup is enabled. The removal is not forced:
// if the image is still used by another container, Docker refuses and the
// caller logs it.
func RemoveImage(ctx context.Context, cli *client.Client, imageID string) error {
	_, err := cli.ImageRemove(ctx, imageID, image.RemoveOptions{})
	return err
}
