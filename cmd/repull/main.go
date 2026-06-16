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

// version is set at build time via -ldflags.
var version = "dev"

// Environment variables provide the flag defaults, so an explicit flag
// always wins over its environment variable.
var (
	interval       = flag.Int("interval", envInt("REPULL_INTERVAL"), "Run every N seconds (0 = single run)")
	schedule       = flag.String("schedule", os.Getenv("REPULL_SCHEDULE"), "Run at specific time daily (HH:MM format, e.g., 23:00)")
	dryRun         = flag.Bool("dry-run", os.Getenv("REPULL_DRY_RUN") == "true", "Show what would be updated without making changes")
	cleanup        = flag.Bool("cleanup", os.Getenv("REPULL_CLEANUP") == "true", "Remove the replaced image after a successful update")
	dockerHost     = flag.String("docker-host", "", "Docker daemon socket (default: from DOCKER_HOST env)")
	discordWebhook = flag.String("discord-webhook", os.Getenv("REPULL_DISCORD_WEBHOOK"), "Discord webhook URL for notifications")
)

// envInt parses an integer environment variable for use as a flag default.
// An unset variable yields 0; an invalid value is fatal — silently falling
// back to 0 would turn a typo into an unintended single-run mode.
func envInt(name string) int {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("[ERROR] Invalid %s %q: must be a number of seconds", name, v)
	}
	return n
}

func main() {
	flag.Parse()

	// Validate: interval and schedule are mutually exclusive
	if *interval > 0 && *schedule != "" {
		log.Fatal("[ERROR] Cannot use --interval and --schedule together")
	}

	// Validate: interval must be at least 60 seconds to avoid hammering
	// registries. Also catches negative values, which would otherwise fall
	// through to single-run mode silently.
	if *interval != 0 && *interval < 60 {
		log.Fatal("[ERROR] --interval must be at least 60 seconds (or 0 for a single run)")
	}

	// Validate the schedule up front so a typo fails fast, before any Docker
	// connection or leftover cleanup happens.
	var targetTime time.Time
	if *schedule != "" {
		var err error
		targetTime, err = parseScheduleTime(*schedule)
		if err != nil {
			log.Fatalf("[ERROR] Invalid schedule format: %v (use HH:MM)", err)
		}
	}

	log.Printf("[INFO] Repull %s starting...", version)

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

	// Remove containers left behind by a previous self-update.
	if !*dryRun {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		removed, cleanupErr := docker.CleanupSelfUpdateLeftovers(cleanupCtx, cli)
		cleanupCancel()
		if cleanupErr != nil {
			log.Printf("[WARN] Failed to clean up self-update leftovers: %v", cleanupErr)
		} else if len(removed) > 0 {
			log.Printf("[INFO] Removed %d stale repull container(s): %s", len(removed), strings.Join(removed, ", "))
		}
	}

	// Create Discord notifier
	notifier, err := notify.NewDiscordNotifier(*discordWebhook)
	if err != nil {
		log.Fatalf("[ERROR] %v", err)
	}
	if notifier != nil {
		log.Println("[INFO] Discord notifications enabled")
	}

	if *dryRun {
		log.Println("[INFO] Running in DRY-RUN mode - no changes will be made")
	}
	if *cleanup {
		log.Println("[INFO] Cleanup enabled - replaced images will be removed after updates")
	}

	// Run based on mode
	if *schedule != "" {
		log.Printf("[INFO] Running in schedule mode (daily at %s)", *schedule)
		runSchedule(cli, notifier, targetTime)
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
	// Listing and inspecting containers is fast; a short deadline prevents a
	// stalled Docker daemon from blocking the loop indefinitely. The update
	// work itself is bounded per group inside UpdateGroups, so one slow group
	// cannot eat the time budget of the others.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

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

	// Update groups. Deliberately not bound to the listing deadline above —
	// UpdateGroups applies its own per-group timeout.
	return updater.UpdateGroups(context.Background(), cli, groups, *dryRun, *cleanup, notifier)
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

// runSchedule runs the update check daily at targetTime's wall-clock time.
func runSchedule(cli *client.Client, notifier *notify.Notifier, targetTime time.Time) {
	for {
		// Calculate time until next occurrence
		next := nextOccurrence(targetTime, time.Now())

		log.Printf("[INFO] Next run scheduled at %s (in %s)", next.Format("2006-01-02 15:04:05"), time.Until(next).Round(time.Second))

		// Sleep in short chunks and re-check the wall clock. time.Sleep uses
		// the monotonic clock, so a single long sleep overshoots the target
		// when the machine suspends or the clock is adjusted; chunked sleeping
		// keeps the run within a minute of the scheduled wall-clock time.
		for {
			remaining := time.Until(next)
			if remaining <= 0 {
				break
			}
			if remaining > time.Minute {
				remaining = time.Minute
			}
			time.Sleep(remaining)
		}

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

// nextOccurrence calculates the next occurrence of target's wall-clock time
// strictly after now.
func nextOccurrence(target time.Time, now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), target.Hour(), target.Minute(), 0, 0, now.Location())

	// If target time already passed today, schedule for tomorrow.
	// Day()+1 is calendar-aware: unlike Add(24h), it lands on the same
	// wall-clock time across DST transitions (where a day is 23 or 25 hours).
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, target.Hour(), target.Minute(), 0, 0, now.Location())
	}

	return next
}
