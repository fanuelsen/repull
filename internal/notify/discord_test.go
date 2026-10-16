package notify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscordRequestErrorsDoNotLogWebhookSecrets(t *testing.T) {
	const token = "FAKE_WEBHOOK_SECRET"
	const webhook = "https://discord.com/api/webhooks/123/" + token
	for _, tt := range []struct {
		name    string
		url     string
		failure string
	}{
		{name: "connection error", url: webhook, failure: "simulated DNS failure"},
		{name: "transport echoes URL", url: webhook, failure: "could not contact " + webhook},
		{name: "transport echoes token", url: webhook, failure: "request failed for " + token},
		{name: "invalid URL", url: webhook + "%invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			previousClient, previousOutput := httpClient, log.Writer()
			t.Cleanup(func() { httpClient = previousClient; log.SetOutput(previousOutput) })
			var logs bytes.Buffer
			log.SetOutput(&logs)
			httpClient = &http.Client{Transport: notificationTransport(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(tt.failure)
			})}
			n, err := NewDiscordNotifier(tt.url)
			if err != nil {
				t.Fatal(err)
			}
			n.SendUpdate("app", "app:latest", "old", "new")
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "https://") {
				t.Fatal("notification failure logged a secret or URL")
			}
			if !strings.Contains(logs.String(), "Discord notification failed") {
				t.Fatal("notification failure was not logged")
			}
		})
	}
}

func TestDiscordResponseStatusLogging(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			previousClient, previousOutput := httpClient, log.Writer()
			t.Cleanup(func() { httpClient = previousClient; log.SetOutput(previousOutput) })
			var logs bytes.Buffer
			log.SetOutput(&logs)
			httpClient = &http.Client{Transport: notificationTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			n, err := NewDiscordNotifier("https://discord.com/api/webhooks/123/FAKE_WEBHOOK_SECRET")
			if err != nil {
				t.Fatal(err)
			}
			n.SendError("app", "update failed")
			if status == http.StatusNoContent && logs.Len() != 0 {
				t.Errorf("success logged an error: %s", logs.String())
			}
			if status == http.StatusTooManyRequests && !strings.Contains(logs.String(), "status 429") {
				t.Errorf("missing HTTP status diagnostic: %s", logs.String())
			}
		})
	}
}
