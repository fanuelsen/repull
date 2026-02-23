<div align="center">
  <img src="assets/logo.png" alt="Repull Logo" width="400"/>
</div>

# Repull

A minimal Docker container auto-updater, a Watchtower alternative. Automatically updates running containers when new images are available.

**Philosophy:** Keep It Simple, Stupid (KISS) - No web UI, no config files, no database.

**Why did i build this when so many other projects exists?** 
First of all, Watchtower was archived, so I needed a new way to automatically pull Docker image updates.Most of the other projects I found felt way more complex than what I wanted. I just wanted a small application that updates images to the latest version and sends a notification when it’s done.
So I started this project. The philosophy is to keep the application as small as possible, to avoid introducing security issues, and to only use the libraries I actually need. Right now it only depends on the Go Docker library, and I want to keep it that way.
I’m not a programming expert, so I also used help from Claude Code, Reddit, and Google to build this. I made this application mostly for fun. If you find a bug, please open an issue. If you want a new feature, you’ll probably want to fork it, as I don’t intend to make it more complex.
I use Renovate and govulncheck to keep dependencies up to date and to check for vulnerable or compromised packages. This is automated in my homelab, and I’ll release new versions when updates are needed. If you're a Go expert and notice any improvements or logical errors, please open an issue. I’d really appreciate the help.

## Features

- Opt-in only via `io.repull.enable=true` label
- Docker Compose aware (groups services correctly)
- Multi-network container support
- Preserves all container config (volumes, ports, networks, env vars, etc.)
- Interval or scheduled updates
- Discord webhook notifications
- Dry-run mode
- Single static binary, no dependencies

## Quick Start

### Docker Compose with Socket Proxy (recommended)

```yaml
services:
  repull:
    image: fanuelsen/repull
    container_name: repull
    restart: unless-stopped
    environment:
      - DOCKER_HOST=tcp://socket-proxy:2375
      - REPULL_INTERVAL=300  # Check every 5 minutes
      # - REPULL_SCHEDULE=23:00  # Or run daily at 11 PM
      # - REPULL_DISCORD_WEBHOOK=https://discord.com/api/webhooks/...
    networks:
      - socket-proxy
      # Add an external network if using Discord webhooks (needs internet)
    labels:
      - "io.repull.enable=true"

  socket-proxy:
    image: tecnativa/docker-socket-proxy
    container_name: socket-proxy
    restart: unless-stopped
    privileged: true
    userns_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    environment:
      CONTAINERS: 1
      IMAGES: 1
      NETWORKS: 1
      POST: 1
    networks:
      - socket-proxy

networks:
  socket-proxy:
    internal: true  # No internet access needed for pulling images
```

**Note:** Repull doesn't need internet access for pulling images - the Docker daemon on the host does the actual pulling. Repull only needs internet if you're using Discord webhook notifications.

### Binary

Download from [GitHub Releases](https://github.com/fanuelsen/repull/releases):

```bash
chmod +x repull
sudo mv repull /usr/local/bin/

# Run once
repull

# Run every 5 minutes
repull --interval 300

# Run daily at 11 PM
repull --schedule 23:00
```

## Usage

### 1. Label Your Containers

Add `io.repull.enable=true` to containers you want auto-updated:

```yaml
services:
  app:
    image: myapp:latest
    labels:
      io.repull.enable: "true"
```

| Label | Value | Description |
|-------|-------|-------------|
| `io.repull.enable` | `true` | Opt this container in to auto-updates |

### 2. Run Repull

```bash
# Single check
repull

# Continuous (every 5 minutes)
repull --interval 300

# Scheduled (daily at specific time)
repull --schedule 23:00

# With Discord notifications
repull --interval 300 --discord-webhook "https://discord.com/api/webhooks/..."

# Dry-run (preview only)
repull --dry-run
```

## Configuration

| Flag | Env Variable | Description |
|------|--------------|-------------|
| `--interval N` | `REPULL_INTERVAL` | Run every N seconds (0 = single run) |
| `--schedule HH:MM` | `REPULL_SCHEDULE` | Run daily at specific time |
| `--discord-webhook URL` | `REPULL_DISCORD_WEBHOOK` | Discord webhook for notifications |
| `--dry-run` | `REPULL_DRY_RUN` | Preview changes without applying |
| `--docker-host HOST` | `DOCKER_HOST` | Docker daemon address |

**Note:** `--interval` and `--schedule` are mutually exclusive.

**Note:** Prefer `REPULL_DISCORD_WEBHOOK` over `--discord-webhook` for the webhook URL. CLI flags are visible to other processes via `/proc/<pid>/cmdline`, whereas environment variables are not.

## How It Works

1. Lists all running containers
2. Filters for `io.repull.enable=true` label
3. Groups by Docker Compose service
4. Pulls latest image and compares digest
5. Recreates container if digest changed (preserving all config)

## Self-Updates

Repull can update itself. If you add `io.repull.enable=true` to repull's own container, it will pull new images and recreate itself just like any other container. If you don't want repull to self-update, simply don't add the label — repull only touches containers that are explicitly opted in.

## Docker Images

- **Docker Hub:** `fanuelsen/repull:latest`
- **GitHub:** `ghcr.io/fanuelsen/repull:latest`

## Requirements

- Docker Engine (local or remote)
- Docker socket access (`/var/run/docker.sock`)

## Contributing

Want to add a web UI? Kubernetes support? GraphQL API? Please fork it instead. This project is intentionally minimal.
