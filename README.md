# Maxwell

Maxwell is a local orchestration service for torrent -> conversion -> upload pipelines, with a VPN safety gate and both CLI and localhost web UI.

This implementation treats torrent and storage systems as pluggable integrations.

## Supported Integrations

### Torrent providers
- `qbittorrent`
- `transmission`
- `utorrent`

### Storage providers
- `backblaze_b2`
- `aws_s3`
- `google_drive`
- `onedrive`

## Quick Start

1. Create a config file (example below or see [`docs/CONFIG.md`](docs/CONFIG.md)).
2. Run checks:

```bash
maxwell --config ./config.yaml doctor
```

If you use qBittorrent locally and have not configured its WebUI yet, run:

```bash
maxwell --config ./config.yaml torrents setup-qbittorrent
```

3. Add a torrent magnet:

```bash
maxwell --config ./config.yaml torrents add 'magnet:?xt=urn:btih:...'
```

4. Run one processing cycle:

```bash
maxwell --config ./config.yaml run --cycles 1
```

5. List final links:

```bash
maxwell --config ./config.yaml links list
```

6. Start web dashboard:

```bash
maxwell --config ./config.yaml web
```

Web dashboard now includes:
- Add magnet action (same as `torrents add`)
- Run-one-cycle action (same as `run --cycles 1`)
- Live torrents/queue/links/events panes with auto refresh

### qBittorrent one-shot setup

`torrents setup-qbittorrent` will:
- find qBittorrent preferences (`qBittorrent.ini`/`.conf`)
- enable WebUI on localhost
- set/update `torrent.base_url` in Maxwell config
- optionally start qBittorrent and verify API reachability

Options:
- `--port <n>` override WebUI port
- `--start=true|false` start qBittorrent app if needed (default true)
- `--verify=true|false` verify API after setup (default true)

7. Continuous daemon-style loop:

```bash
maxwell --config ./config.yaml run --forever --interval 8s --require-safe-vpn
```

8. Metrics endpoint:

```bash
curl -s http://127.0.0.1:7777/metrics
```

## Example Config

```yaml
vpn:
  mode: enforce
  check_interval_seconds: 8
  require_safe_for_magnet_adds: true

state_store:
  driver: sqlite
  dsn: ./maxwell.db
  max_open_conns: 1

torrent:
  provider: qbittorrent
  base_url: http://127.0.0.1:8080
  username: admin
  password: adminadmin
  download_dir: ./downloads
  category: maxwell

storage:
  provider: backblaze_b2
  endpoint: http://127.0.0.1:9000
  bucket: media
  key_id: my-key-id
  app_key: my-app-key
  public_base_url: https://cdn.example.com

ffmpeg:
  bin: copy
  ffprobe_bin: ffprobe
  preset: h264_1080p_fast

paths:
  downloads_dir: ./downloads
  processed_dir: ./processed

workers:
  conversion: 1
  upload: 1
  max_attempts: 5
  backoff_seconds: 5

security:
  web_bind: 127.0.0.1:7777
  web_token: change-me
  csrf_enabled: true
```

## Testing

The repository includes:
- Unit tests (`internal/...`)
- Integration tests (`test/integration`)
- End-to-end smoke (`test/e2e`)
- CLI tests (`internal/cli`)
- Web UI handler tests (`internal/web`)

Run when Go is installed:

```bash
go test ./...
```

## Notes

- VPN checks currently use a pluggable detector; local runs can force a state with `MAXWELL_VPN_FORCE_STATE=SAFE|UNSAFE|UNKNOWN`.
- `ffmpeg.bin: copy` uses an internal copy converter for deterministic local testing.
