# Operations

## Common flows

### Run one cycle
```bash
maxwell --config ./config.yaml run --cycles 1
```

### Run continuously (daemon mode)
```bash
maxwell --config ./config.yaml run --forever --interval 8s --require-safe-vpn
```

### Start web UI
```bash
maxwell --config ./config.yaml web
```

### Scrape local metrics
```bash
curl -s http://127.0.0.1:7777/metrics
```

### Check VPN state
```bash
maxwell --config ./config.yaml vpn status
```

### Auto-configure qBittorrent WebUI
```bash
maxwell --config ./config.yaml torrents setup-qbittorrent
```

## Recovery

### Restart-safe queues
Conversion and upload jobs are persisted in SQLite. Re-running `run` will continue queued jobs.

### Re-drive pipeline from completed torrents
Run additional cycles to re-sync provider state and queue missing conversion/upload work.

### Failed jobs
Failed conversion/upload jobs are stored with error messages in queue tables and event logs (`/api/events`).
Retries are automatic with exponential backoff up to `workers.max_attempts`.

## Local test mode
Set VPN state for deterministic testing:

```bash
export MAXWELL_VPN_FORCE_STATE=SAFE
```

Allowed values: `SAFE`, `UNSAFE`, `UNKNOWN`.
