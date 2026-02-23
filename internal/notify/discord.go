package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// httpClient is used for all Discord webhook requests.
// A 10s timeout prevents a hung Discord connection from stalling the update loop.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// Notifier sends notifications to Discord via webhook
type Notifier struct {
	webhookURL string
}

// NewDiscordNotifier creates a new Discord notifier.
// Returns nil if webhookURL is empty (disables notifications).
// Returns an error if the URL is not a valid Discord webhook.
func NewDiscordNotifier(webhookURL string) (*Notifier, error) {
	if webhookURL == "" {
		return nil, nil
	}
	if !strings.HasPrefix(webhookURL, "https://discord.com/api/webhooks/") &&
		!strings.HasPrefix(webhookURL, "https://discordapp.com/api/webhooks/") {
		return nil, fmt.Errorf("invalid Discord webhook URL: must start with https://discord.com/api/webhooks/")
	}
	return &Notifier{webhookURL: webhookURL}, nil
}

// SendUpdate sends a notification about a successful container update
func (n *Notifier) SendUpdate(service, image, oldDigest, newDigest string) error {
	if n == nil {
		return nil
	}

	message := map[string]interface{}{
		"content": fmt.Sprintf("✅ Updated %s\nImage: %s\n%s → %s",
			service, image, truncate(oldDigest), truncate(newDigest)),
	}

	return n.send(message)
}

// SendError sends a notification about an update failure.
// Error messages are truncated to avoid leaking sensitive data (e.g. registry
// credentials that may appear in Docker API error strings) to Discord.
func (n *Notifier) SendError(service, errorMsg string) error {
	if n == nil {
		return nil
	}

	const maxLen = 200
	if len(errorMsg) > maxLen {
		errorMsg = errorMsg[:maxLen] + "..."
	}

	message := map[string]interface{}{
		"content": fmt.Sprintf("❌ Failed to update %s\nError: %s", service, errorMsg),
	}

	return n.send(message)
}

// send performs the HTTP POST to Discord webhook
func (n *Notifier) send(message map[string]interface{}) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}

	resp, err := httpClient.Post(n.webhookURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// truncate shortens a digest for display
func truncate(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "..."
	}
	return digest
}
