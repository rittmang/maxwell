# Configuration

## Top-level sections
- `vpn`
- `torrent`
- `storage`
- `ffmpeg`
- `paths`
- `workers`
- `security`
- `state_store`
- `database` (legacy fallback)

## `torrent`
- `provider`: `qbittorrent` | `transmission` | `utorrent`
- `base_url`: provider API base URL
- `username`: optional
- `password`: optional
- `download_dir`: path where provider should save downloads
- `category`: optional category/tag

For local qBittorrent setup, you can auto-configure WebUI and `torrent.base_url` with:

```bash
maxwell --config ./config.yaml torrents setup-qbittorrent
```

## `storage`
- `provider`: `backblaze_b2` | `aws_s3` | `google_drive` | `onedrive`
- `endpoint`: required for `backblaze_b2` and `aws_s3`
- `bucket`: required for `backblaze_b2` and `aws_s3`
- `key_id`: optional credential key for s3-compatible providers
- `app_key`: optional credential secret for s3-compatible providers
- `public_base_url`: optional final link base for s3-compatible providers
- `access_token`: required for `google_drive` and `onedrive`
- `drive_id`: optional

## `vpn`
- `mode`: optional (`enforce` by default)
- `require_safe_for_magnet_adds`: if true, magnet adds are blocked unless VPN state is `SAFE`
- `allowed_tunnel_if_prefixes`, `home_ssids`, `home_cidrs`, `home_public_ips`, `home_asns`, `allowed_vpn_asns`: optional policy inputs

## `ffmpeg`
- `bin`: `copy` (test-friendly) or actual ffmpeg binary path
- `ffprobe_bin`: optional path
- `preset`: `h264_1080p_fast` (default) or `h265_1080p_balanced`
- `output_dir`: optional output path

## `paths`
- `downloads_dir`
- `processed_dir`

## `workers`
- `conversion` > 0
- `upload` > 0
- `max_attempts` > 0
- `backoff_seconds` >= 0

## `security`
- `web_bind`: e.g. `127.0.0.1:7777`
- `web_token`: required if mutating web endpoints should be protected
- `csrf_enabled`: default true

## `state_store`
- `driver`: `sqlite` | `postgres` | `mysql`
- `dsn`: sqlite file path or SQL DSN
- `max_open_conns`: connection cap

## `database`
- `path`: sqlite file path
- legacy compatibility field; `state_store.dsn` is preferred
