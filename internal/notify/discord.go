package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Notifier sends notifications to Discord via webhook
type Notifier struct {
	webhookURL string
}

// NewDiscordNotifier creates a new Discord notifier.
// Returns nil if webhookURL is empty (disables notifications).
func NewDiscordNotifier(webhookURL string) *Notifier {
	if webhookURL == "" {
		return nil
	}
	return &Notifier{webhookURL: webhookURL}
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

// SendError sends a notification about an update failure
func (n *Notifier) SendError(service, errorMsg string) error {
	if n == nil {
		return nil
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

	resp, err := http.Post(n.webhookURL, "application/json", bytes.NewBuffer(data))
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
