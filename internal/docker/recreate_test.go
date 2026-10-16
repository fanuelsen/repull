package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type recreateTransport func(*http.Request) (*http.Response, error)

func (f recreateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func dockerResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Exercise the real SDK request flow without a Docker daemon. In particular,
// stopping an AutoRemove container must never be followed by renaming or
// restarting the old ID, and creating its replacement must wait for removal.
func TestRecreateContainer(t *testing.T) {
	for _, tt := range []struct {
		name          string
		autoRemove    bool
		removedOnStop bool
		stopStatus    int
		waitStatus    int
		waitError     bool
		cancelWait    bool
		createFails   bool
		startFails    bool
		wantError     string
		wantRollback  bool
	}{
		{name: "auto-remove already removed", autoRemove: true, removedOnStop: true},
		{name: "auto-remove waits for removal", autoRemove: true},
		{name: "auto-remove missing at stop", autoRemove: true, stopStatus: 404},
		{name: "auto-remove stop failure", autoRemove: true, stopStatus: 500, wantError: "failed to stop"},
		{name: "auto-remove wait HTTP failure", autoRemove: true, waitStatus: 500, wantError: "failed to wait"},
		{name: "auto-remove wait response failure", autoRemove: true, waitError: true, wantError: "removal failed"},
		{name: "auto-remove wait canceled", autoRemove: true, cancelWait: true, wantError: "context canceled"},
		{name: "auto-remove creation failure", autoRemove: true, createFails: true, wantError: "rollback unavailable"},
		{name: "auto-remove start failure", autoRemove: true, startFails: true, wantError: "rollback unavailable"},
		{name: "normal update"},
		{name: "normal creation failure rolls back", createFails: true, wantError: "failed to create", wantRollback: true},
		{name: "normal start failure rolls back", startFails: true, wantError: "failed to start", wantRollback: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const oldID = "abcdef1234567890"
			const newID = "replacement"
			old := container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					ID: oldID, Name: "/app", Image: "sha256:old",
					HostConfig: &container.HostConfig{AutoRemove: tt.autoRemove, NetworkMode: "bridge"},
				},
				Config: &container.Config{Image: "app:latest", Env: []string{"APP_SETTING=custom"}},
			}
			inspected, stopped, removed, renamed := false, false, false, false
			created, newRunning, rolledBack, newRemoved := false, false, false, false
			transport := recreateTransport(func(r *http.Request) (*http.Response, error) {
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				switch {
				case r.Method == "GET" && strings.HasPrefix(path, "/images/"):
					if stopped {
						t.Error("replacement config read after stop; the old container may already be removed")
					}
					inspected = true
					return dockerResponse(200, `{"Id":"sha256:old","Config":{}}`), nil
				case r.Method == "POST" && path == "/containers/"+oldID+"/stop":
					if !inspected {
						t.Error("stop requested before preparing replacement config")
					}
					if r.URL.Query().Has("t") {
						t.Error("stop must retain the container's own stop timeout")
					}
					if tt.stopStatus == 500 {
						return dockerResponse(500, `{"message":"stop failed"}`), nil
					}
					stopped = true
					removed = tt.removedOnStop || tt.stopStatus == 404
					if tt.stopStatus == 404 {
						return dockerResponse(404, `{"message":"No such container"}`), nil
					}
					return dockerResponse(204, ""), nil
				case r.Method == "POST" && path == "/containers/"+oldID+"/wait":
					if !tt.autoRemove || !stopped || r.URL.Query().Get("condition") != "removed" {
						t.Error("must wait for removal after stopping an AutoRemove container")
					}
					if tt.cancelWait {
						cancel()
						return nil, r.Context().Err()
					}
					if tt.waitStatus == 500 {
						return dockerResponse(500, `{"message":"wait failed"}`), nil
					}
					if tt.waitError {
						return dockerResponse(200, `{"StatusCode":0,"Error":{"Message":"removal failed"}}`), nil
					}
					if removed {
						return dockerResponse(404, `{"message":"No such container"}`), nil
					}
					removed = true
					// Non-zero exit is expected when stopping some applications;
					// only the wait error, not the exit code, prevents recreation.
					return dockerResponse(200, `{"StatusCode":137}`), nil
				case r.Method == "POST" && path == "/containers/"+oldID+"/rename":
					if tt.autoRemove {
						t.Error("must not rename an AutoRemove container after stop")
						return dockerResponse(404, `{"message":"No such container"}`), nil
					}
					renamed = r.URL.Query().Get("name") != old.Name
					return dockerResponse(204, ""), nil
				case r.Method == "POST" && path == "/containers/create":
					created = true
					if !stopped || (tt.autoRemove && !removed) || (!tt.autoRemove && !renamed) {
						t.Error("replacement created before the old container released its name")
					}
					var config container.CreateRequest
					if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
						t.Fatal(err)
					}
					if config.HostConfig.AutoRemove != tt.autoRemove || config.Image != "app:latest" || r.URL.Query().Get("name") != old.Name {
						t.Error("replacement did not preserve AutoRemove, image reference, and name")
					}
					if len(config.Env) != 1 || config.Env[0] != "APP_SETTING=custom" {
						t.Errorf("replacement lost environment: %v", config.Env)
					}
					if tt.createFails {
						return dockerResponse(500, `{"message":"create failed"}`), nil
					}
					return dockerResponse(201, `{"Id":"replacement"}`), nil
				case r.Method == "POST" && path == "/containers/"+newID+"/start":
					if tt.startFails {
						return dockerResponse(500, `{"message":"start failed"}`), nil
					}
					newRunning = true
					return dockerResponse(204, ""), nil
				case r.Method == "POST" && path == "/containers/"+oldID+"/start":
					if removed || tt.autoRemove || renamed {
						t.Error("rollback attempted on a removed or still-renamed container")
					}
					rolledBack = true
					return dockerResponse(204, ""), nil
				case r.Method == "DELETE" && path == "/containers/"+newID:
					newRemoved = true
					return dockerResponse(204, ""), nil
				case r.Method == "DELETE" && path == "/containers/"+oldID:
					if tt.autoRemove || !newRunning {
						t.Error("old container removed unnecessarily or before replacement started")
					}
					removed = true
					return dockerResponse(204, ""), nil
				default:
					return nil, fmt.Errorf("unexpected request: %s %s", r.Method, path)
				}
			})
			cli, err := client.NewClientWithOpts(client.WithHost("http://docker.invalid"), client.WithVersion("1.47"), client.WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			id, err := RecreateContainer(ctx, cli, old, nil)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if !tt.createFails && !tt.startFails && created {
					t.Error("created replacement despite stop/wait failure")
				}
			} else if err != nil || id != newID || !newRunning || !removed {
				t.Fatalf("update failed: id=%q err=%v running=%v removed=%v", id, err, newRunning, removed)
			}
			if rolledBack != tt.wantRollback {
				t.Errorf("rollback = %v, want %v", rolledBack, tt.wantRollback)
			}
			if tt.startFails && !newRemoved {
				t.Error("failed replacement was not cleaned up")
			}
		})
	}
}
