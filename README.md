<div align="center">
  <img src="assets/logo.png" alt="Repull Logo" width="200"/>
</div>

# Repull

A minimal, KISS-friendly Docker container auto-updater. Automatically updates running containers when new images are available, with support for Docker Compose.

## Why Repull?

Repull is an alternative to Watchtower that follows the KISS (Keep It Simple, Stupid) principle:

- **No web UI** - Command-line only
- **No config files** - CLI flags and environment variables
- **Stateless** - No database, no persistent state
- **Opt-in only** - Containers must be explicitly labeled
- **Compose-aware** - Understands Docker Compose services via runtime labels
- **Safe** - Updates one service at a time, preserves container configuration

Perfect for single-host Docker setups where you want simple, reliable auto-updates.

## Features

- ✅ Detects image updates using digest comparison (not tags)
- ✅ Automatically recreates containers when images change
- ✅ Preserves container configuration (volumes, ports, networks, env vars)
- ✅ Groups Docker Compose services correctly
- ✅ Dry-run mode to preview changes
- ✅ Interval mode for continuous monitoring
- ✅ Scheduled updates (cron-like, run daily at specific time)
- ✅ Discord notifications (optional, webhook-based)
- ✅ Single static binary (~570 LOC)
- ✅ No external dependencies (pure Go stdlib)

## Installation

### Option 1: Download Binary

```bash
# Build from source
git clone https://github.com/fanuelsen/repull.git
cd repull
go build -o repull ./cmd/repull

# Move to PATH
sudo mv repull /usr/local/bin/
```

### Option 2: Run as Docker Container (Recommended)

```bash
docker build -t repull .

# Run once
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock repull

# Run continuously (check every 5 minutes)
docker run -d \
  --name repull \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  repull --interval 300

# Using environment variables
docker run -d \
  --name repull \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e REPULL_INTERVAL=300 \
  -e REPULL_DISCORD_WEBHOOK="https://discord.com/api/webhooks/..." \
  repull
```

### Option 3: Docker Compose

```yaml
services:
  repull:
    build: .
    container_name: repull
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - REPULL_SCHEDULE=23:00
      - REPULL_DISCORD_WEBHOOK=${DISCORD_WEBHOOK}
    restart: unless-stopped
```

## Usage

### Label Your Containers

Add the `io.repull.enable=true` label to containers you want to auto-update.

**Docker run:**
```bash
docker run -d \
  --label io.repull.enable=true \
  --name myapp \
  nginx:latest
```

**Docker Compose:**
```yaml
version: '3.8'

services:
  web:
    image: nginx:latest
    labels:
      io.repull.enable: "true"
    ports:
      - "80:80"
    volumes:
      - ./html:/usr/share/nginx/html

  db:
    image: postgres:15
    labels:
      io.repull.enable: "true"
    environment:
      POSTGRES_PASSWORD: secret
    volumes:
      - db-data:/var/lib/postgresql/data

volumes:
  db-data:
```

### Run Repull

**Single check:**
```bash
repull
```

**Continuous monitoring (every 5 minutes):**
```bash
repull --interval 300
```

**Scheduled daily updates (e.g., at 11 PM):**
```bash
repull --schedule 23:00
```

**With Discord notifications:**
```bash
repull --interval 300 --discord-webhook "https://discord.com/api/webhooks/..."
```

**Dry-run (preview changes):**
```bash
repull --dry-run
```

**Remote Docker daemon:**
```bash
repull --docker-host tcp://remote-host:2375
# Or use environment variable
export DOCKER_HOST=tcp://remote-host:2375
repull
```

### CLI Flags

| Flag | Env Variable | Default | Description |
|------|--------------|---------|-------------|
| `--interval` | `REPULL_INTERVAL` | 0 | Run every N seconds (0 = single run) |
| `--schedule` | `REPULL_SCHEDULE` | "" | Run at specific time daily (HH:MM format, e.g., 23:00) |
| `--discord-webhook` | `REPULL_DISCORD_WEBHOOK` | "" | Discord webhook URL for notifications |
| `--dry-run` | `REPULL_DRY_RUN` | false | Show what would be updated without making changes |
| `--docker-host` | `DOCKER_HOST` | from env | Docker daemon socket |

**Note:** `--interval` and `--schedule` are mutually exclusive. Environment variables are overridden by CLI flags.

## How It Works

1. **List** running containers
2. **Filter** containers with `io.repull.enable=true` label
3. **Group** containers by Docker Compose project/service
4. **Pull** latest image for each group
5. **Compare** image digests (old vs new)
6. **Recreate** containers if digest changed:
   - Stop old container
   - Remove old container
   - Create new container with same config
   - Start new container

All container configuration is preserved: volumes, ports, networks, environment variables, labels, restart policies.

## Example Output

```
[INFO] Repull starting...
[INFO] Connected to Docker daemon
[INFO] Running in single-run mode
[INFO] Found 5 running container(s)
[INFO] Found 3 opted-in container(s) (label: io.repull.enable=true)
[INFO] Grouped into 2 service(s)
[INFO] Checking myapp:web (2 container(s))
[INFO] Pulling image nginx:latest
[INFO] Image digest unchanged, skipping myapp:web
[INFO] Checking myapp:db (1 container(s))
[INFO] Pulling image postgres:15
[INFO] Image digest changed: sha256:abc123... -> sha256:def456...
[INFO] Recreating 1 container(s)
[INFO] Recreating container /myapp-db-1
[INFO] Successfully recreated /myapp-db-1
[INFO] Update complete
```

## Discord Notifications

Get notified when containers are updated or when updates fail:

1. **Create a Discord webhook:**
   - Go to your Discord server settings
   - Select "Integrations" → "Webhooks"
   - Create a new webhook and copy the URL

2. **Run repull with the webhook:**
   ```bash
   repull --interval 300 --discord-webhook "https://discord.com/api/webhooks/..."
   ```

3. **Or use environment variables:**
   ```bash
   export DISCORD_WEBHOOK="https://discord.com/api/webhooks/..."
   repull --schedule 23:00 --discord-webhook "$DISCORD_WEBHOOK"
   ```

**Notifications are sent for:**
- ✅ Successful container updates (shows old → new digest)
- ❌ Failed updates (shows error message)

**Example notification:**
```
✅ Updated repull:test-nginx
Image: nginx:latest
sha256:db3459... → sha256:fb0111...
```

## Scheduled Updates

Run repull at a specific time each day instead of on an interval:

```bash
# Run daily at 11 PM
repull --schedule 23:00

# Run daily at 3 AM with notifications
repull --schedule 03:00 --discord-webhook "$DISCORD_WEBHOOK"
```

The scheduler:
- Uses 24-hour format (HH:MM)
- Runs once per day at the specified time
- Uses local system timezone
- Cannot be combined with `--interval`

## Docker Compose Integration

Repull doesn't parse `docker-compose.yml` files. Instead, it uses runtime labels that Docker Compose automatically adds:

- `com.docker.compose.project` - Project name
- `com.docker.compose.service` - Service name

All containers in the same service are updated together, even if you scale to multiple replicas.

## Safety

- Updates **one service at a time** (sequential, not parallel)
- **Fail-fast** on Docker API errors
- **Dry-run mode** to preview changes
- Only updates containers you explicitly opt-in
- Never modifies volumes or networks

## Non-Features (By Design)

Repull intentionally does NOT include:

- Notifications (email, webhooks, etc.)
- Web UI
- Docker Swarm support
- Parsing docker-compose.yml files
- Running `docker compose up`
- Automatic rollbacks
- Complex scheduling
- Metrics or logging frameworks
- Update strategies (blue/green, etc.)

If you need these features, consider Watchtower or other tools.

## Requirements

- Docker Engine (local or remote)
- Go 1.21+ (for building from source)
- Docker API access (via socket or TCP)

## Security

Repull takes security seriously. See [SECURITY.md](SECURITY.md) for our security policy.

**Quick security commands:**

```bash
# Check for vulnerabilities
make vuln-check

# Run full security audit
make audit

# Update dependencies
make update-deps
```

**Automated monitoring:**
- ✅ Weekly vulnerability scans via GitHub Actions
- ✅ Dependabot for automated dependency updates
- ✅ Official Go vulnerability database (govulncheck)

**Current status:** No known vulnerabilities

## License

MIT License - See LICENSE file for details

## Contributing

Contributions are welcome! Please keep changes minimal and aligned with the KISS philosophy.

Before adding features, ask: "Is this absolutely necessary?" If not, it probably shouldn't be added.

## Troubleshooting

**Containers not updating?**
- Check that the label is set: `docker inspect <container> | grep io.repull.enable`
- Make sure the image digest actually changed (not just the tag)

**Permission denied errors?**
- Ensure the Docker socket is accessible: `/var/run/docker.sock`
- When running in Docker, mount the socket: `-v /var/run/docker.sock:/var/run/docker.sock`

**Connection refused?**
- Check DOCKER_HOST environment variable
- Verify Docker daemon is running

## Comparison with Watchtower

| Feature | Repull | Watchtower |
|---------|--------|------------|
| Auto-updates | ✅ | ✅ |
| Docker Compose | ✅ | ✅ |
| Notifications | ❌ | ✅ |
| Web UI | ❌ | ❌ |
| Swarm support | ❌ | ✅ |
| Complexity | ~500 LOC | 10,000+ LOC |
| Philosophy | KISS | Feature-rich |

Choose Repull if you want simplicity. Choose Watchtower if you need advanced features.
