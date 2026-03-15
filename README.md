# Photo Frame

A self-hosted digital photo frame server. Single Go binary that displays a full-screen slideshow with captions, people tags, and configurable banner messages.

Designed to run on any Linux box (Raspberry Pi, NUC, old laptop) connected to a TV or monitor. Photos and metadata are synced from a remote host via HTTP API.

## Features

- Full-screen slideshow with smooth crossfade transitions
- AI-generated captions and people tags overlay
- Configurable banner messages (e.g. "Call me back", "Happy Birthday!")
- Settings page for slideshow speed, banner text, quick-message buttons
- Sync API for pushing photos and metadata from a remote host
- Token-based authentication for sync operations
- SQLite database for photo metadata
- Health endpoint for monitoring
- Clock overlay
- Auto-hides cursor during slideshow
- Single binary deployment (Go embed for static files)

## Quick Start

### Prerequisites

- Go 1.21+ (for building from source)
- GCC (required for SQLite via cgo)

### Build

```bash
CGO_ENABLED=1 go build -o photo-frame .
```

### Run

```bash
# Basic usage (photos in ./photos, config in ./data, port 8085)
./photo-frame

# With custom settings
PORT=8080 PHOTO_DIR=/mnt/photos SYNC_TOKEN=mysecret ./photo-frame
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8085` | HTTP server port |
| `PHOTO_DIR` | `./photos` | Directory for storing photo files |
| `CONFIG_DIR` | `./data` | Directory for SQLite database and config |
| `SYNC_TOKEN` | `changeme` | Authentication token for sync API |

## Usage

### Viewing Photos

Open `http://your-host:8085/` in a browser (ideally full-screen on a TV/monitor). The slideshow will start automatically once photos are synced.

### Settings

Open `http://your-host:8085/settings` to:
- Set or clear banner messages
- Adjust slideshow speed (seconds per photo)
- View photo count and uptime

### Syncing Photos

Photos are pushed to the frame via the sync API. The included `sync_push.py` script handles this from a remote host.

#### Using sync_push.py

```bash
# Set environment
export FRAME_HOST=192.168.1.100:8085
export SYNC_TOKEN=mysecret

# Sync all photos and metadata
python3 sync_push.py

# Metadata only (no file transfer)
python3 sync_push.py --metadata-only

# Dry run (show what would be synced)
python3 sync_push.py --dry-run
```

The sync script reads from a SQLite captions database (photo_captions.db) and pushes photo metadata and files to the frame. Modify the `PHOTO_ROOTS` and `CAPTIONS_DB` paths in the script to match your setup.

#### API Endpoints

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/` | GET | No | Slideshow page |
| `/settings` | GET | No | Settings page |
| `/api/config` | GET | No | Get current config |
| `/api/config` | PUT | No | Update config |
| `/api/photos` | GET | No | Get random photos with metadata |
| `/api/image/{path}` | GET | No | Serve a photo file |
| `/api/sync` | POST | Yes | Sync photo metadata (batch) |
| `/api/sync/photo` | POST | Yes | Upload a single photo file |
| `/api/health` | GET | No | Health check |

#### Sync API Examples

```bash
# Push metadata
curl -X POST http://host:8085/api/sync \
  -H "Content-Type: application/json" \
  -H "X-Sync-Token: mysecret" \
  -d '{"photos": [{"path": "vacation/beach.jpg", "caption": "Beach sunset", "people": ["Alice", "Bob"]}]}'

# Upload a photo
curl -X POST "http://host:8085/api/sync/photo?path=vacation/beach.jpg" \
  -H "X-Sync-Token: mysecret" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @beach.jpg

# Set a banner
curl -X PUT http://host:8085/api/config \
  -H "Content-Type: application/json" \
  -d '{"banner": "Dinner at 7!", "slideshow_seconds": 15, "enabled": true}'
```

## Deployment

### As a systemd Service

```bash
sudo tee /etc/systemd/system/photo-frame.service << 'EOF'
[Unit]
Description=Photo Frame
After=network.target

[Service]
Type=simple
User=your-user
WorkingDirectory=/opt/photo-frame
Environment=PORT=8085
Environment=PHOTO_DIR=/opt/photo-frame/photos
Environment=CONFIG_DIR=/opt/photo-frame/data
Environment=SYNC_TOKEN=your-secret-token
ExecStart=/opt/photo-frame/photo-frame
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now photo-frame
```

### On a Raspberry Pi with Chromium Kiosk

```bash
# Install Chromium
sudo apt install chromium-browser

# Auto-start in kiosk mode (add to ~/.config/autostart/ or use a script)
chromium-browser --kiosk --disable-restore-session-state http://localhost:8085/
```

### Cross-Compiling for ARM (Raspberry Pi)

```bash
# For Raspberry Pi 3/4 (64-bit)
GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc go build -o photo-frame .

# For Raspberry Pi 2/Zero (32-bit)
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 CC=arm-linux-gnueabihf-gcc go build -o photo-frame .
```

## Testing

```bash
go test -v .
```

## Architecture

```
photo-frame
├── main.go              # Server, API handlers, database
├── main_test.go         # Tests
├── static/
│   ├── frame.html       # Slideshow UI (embedded in binary)
│   └── settings.html    # Settings UI (embedded in binary)
├── sync_push.py         # Remote sync script (runs on photo source host)
├── photos/              # Photo storage (created at runtime)
└── data/
    └── frame.db         # SQLite database (created at runtime)
```

The Go binary embeds the static HTML files at compile time, so the entire application is a single file. No web server or reverse proxy required.

## License

MIT
