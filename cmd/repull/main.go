package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/client"
	"github.com/fanuelsen/repull/internal/docker"
	"github.com/fanuelsen/repull/internal/notify"
	"github.com/fanuelsen/repull/internal/updater"
)

var (
	interval       = flag.Int("interval", 0, "Run every N seconds (0 = single run)")
	schedule       = flag.String("schedule", "", "Run at specific time daily (HH:MM format, e.g., 23:00)")
	dryRun         = flag.Bool("dry-run", false, "Show what would be updated without making changes")
	dockerHost     = flag.String("docker-host", "", "Docker daemon socket (default: from DOCKER_HOST env)")
	discordWebhook = flag.String("discord-webhook", "", "Discord webhook URL for notifications")
)

func main() {
	flag.Parse()

	// Override flags with environment variables if set
	if envInterval := os.Getenv("REPULL_INTERVAL"); envInterval != "" && *interval == 0 {
		if val, err := strconv.Atoi(envInterval); err == nil {
			*interval = val
		}
	}
	if envSchedule := os.Getenv("REPULL_SCHEDULE"); envSchedule != "" && *schedule == "" {
		*schedule = envSchedule
	}
	if envWebhook := os.Getenv("REPULL_DISCORD_WEBHOOK"); envWebhook != "" && *discordWebhook == "" {
		*discordWebhook = envWebhook
	}
	if envDryRun := os.Getenv("REPULL_DRY_RUN"); envDryRun == "true" && !*dryRun {
		*dryRun = true
	}

	// Validate: interval and schedule are mutually exclusive
	if *interval > 0 && *schedule != "" {
		log.Fatal("[ERROR] Cannot use --interval and --schedule together")
	}

	// Validate: interval must be at least 60 seconds to avoid hammering registries
	if *interval > 0 && *interval < 60 {
		log.Fatal("[ERROR] --interval must be at least 60 seconds")
	}

	log.Println("[INFO] Repull starting...")

	// Set DOCKER_HOST if provided via flag
	if *dockerHost != "" {
		os.Setenv("DOCKER_HOST", *dockerHost)
	}

	// Create Docker client
	cli, err := docker.NewClient()
	if err != nil {
		log.Fatalf("[ERROR] Failed to create Docker client: %v", err)
	}
	defer cli.Close()

	log.Println("[INFO] Connected to Docker daemon")

	// Create Discord notifier
	notifier := notify.NewDiscordNotifier(*discordWebhook)
	if notifier != nil {
		log.Println("[INFO] Discord notifications enabled")
	}

	if *dryRun {
		log.Println("[INFO] Running in DRY-RUN mode - no changes will be made")
	}

	// Run based on mode
	if *schedule != "" {
		log.Printf("[INFO] Running in schedule mode (daily at %s)", *schedule)
		runSchedule(cli, notifier)
	} else if *interval > 0 {
		log.Printf("[INFO] Running in loop mode (interval: %d seconds)", *interval)
		runLoop(cli, notifier)
	} else {
		log.Println("[INFO] Running in single-run mode")
		if err := runOnce(cli, notifier); err != nil {
			log.Fatalf("[ERROR] Update failed: %v", err)
		}
		log.Println("[INFO] Update complete")
	}
}

// runOnce performs a single update check and execution.
func runOnce(cli *client.Client, notifier *notify.Notifier) error {
	ctx := context.Background()

	// List running containers
	containers, err := docker.ListRunningContainers(ctx, cli)
	if err != nil {
		return err
	}

	log.Printf("[INFO] Found %d running container(s)", len(containers))

	// Filter opted-in containers
	optedIn := updater.FilterOptedInContainers(containers)
	log.Printf("[INFO] Found %d opted-in container(s) (label: %s=true)", len(optedIn), updater.EnableLabel)

	if len(optedIn) == 0 {
		log.Println("[INFO] No containers opted in for auto-update")
		return nil
	}

	// Group by compose service
	groups := updater.GroupByComposeService(optedIn)
	log.Printf("[INFO] Grouped into %d service(s)", len(groups))

	// Update groups
	return updater.UpdateGroups(ctx, cli, groups, *dryRun, notifier)
}

// runLoop runs the update check in a loop at the specified interval.
func runLoop(cli *client.Client, notifier *notify.Notifier) {
	ticker := time.NewTicker(time.Duration(*interval) * time.Second)
	defer ticker.Stop()

	// Run immediately on start
	log.Println("[INFO] Running initial check...")
	if err := runOnce(cli, notifier); err != nil {
		log.Printf("[ERROR] Update failed: %v", err)
	}

	// Then run on interval
	for range ticker.C {
		log.Printf("[INFO] Running scheduled check (interval: %d seconds)...", *interval)
		if err := runOnce(cli, notifier); err != nil {
			log.Printf("[ERROR] Update failed: %v", err)
		}
		log.Println("[INFO] Check complete, waiting for next interval...")
	}
}

// runSchedule runs the update check daily at a specific time
func runSchedule(cli *client.Client, notifier *notify.Notifier) {
	targetTime, err := parseScheduleTime(*schedule)
	if err != nil {
		log.Fatalf("[ERROR] Invalid schedule format: %v (use HH:MM)", err)
	}

	for {
		// Calculate time until next occurrence
		next := nextOccurrence(targetTime)
		duration := time.Until(next)

		log.Printf("[INFO] Next run scheduled at %s (in %s)", next.Format("2006-01-02 15:04:05"), duration.Round(time.Second))

		// Sleep until target time
		time.Sleep(duration)

		// Run update
		log.Printf("[INFO] Running scheduled check...")
		if err := runOnce(cli, notifier); err != nil {
			log.Printf("[ERROR] Update failed: %v", err)
		}
		log.Println("[INFO] Check complete")
	}
}

// parseScheduleTime parses "HH:MM" format
func parseScheduleTime(schedule string) (time.Time, error) {
	parts := strings.Split(schedule, ":")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid format")
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("invalid hour")
	}

	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("invalid minute")
	}

	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location()), nil
}

// nextOccurrence calculates the next occurrence of target time
func nextOccurrence(target time.Time) time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, now.Location())

	// If target time already passed today, schedule for tomorrow
	if next.Before(now) {
		next = next.Add(24 * time.Hour)
	}

	return next
}
