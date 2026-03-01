# 02-28-2026 Maxwell Go CLI + Local Web Orchestrator Plan

Status
- State: In Progress
- Last updated: 02-28-2026
- Scope target: `maxwell/`

## Objective
Build a production-grade local orchestration service in Go that provides:
1. An interactive CLI.
2. A localhost web interface.
3. A VPN safety gate (kill-switch behavior for torrents).
4. Multi-client torrent download control (`qbittorrent`, `transmission`, `utorrent`).
5. FFmpeg conversion queue processing.
6. Multi-target upload queue processing (`backblaze_b2`, `aws_s3`, `google_drive`, `onedrive`).
7. Final emitted video link per completed upload.
8. Pluggable state-store backend (`sqlite`, `postgres`, `mysql`) for queue/event/link persistence.

## Requirements Mapping

### 0) VPN safety gate before torrenting
- Must detect whether VPN is active across providers (not Mullvad-specific).
- Must detect home network context (home Wi-Fi / home Ethernet).
- If home network is active and VPN is not verifiably active, all torrent downloads are paused and new starts are blocked.

### 1) Show active torrent downloads
- Display active torrents in both CLI and web UI.
- Include name, progress, speed, ETA, state, save path.
- CLI and web must remain functionally equivalent for operator actions (parity matrix enforced by tests).

### 2) Add new magnet link via selected torrent integration
- Submit magnet links through active torrent provider API.
- Force downloads to preconfigured directory.
- Reject or queue additions when VPN gate is in unsafe mode.

### 3) Conversion queue after download
- On completed download, enqueue job for FFmpeg processing.
- Track queue state and retries.
- Run conversion workers on this machine.

### 4) Upload queue after conversion
- Enqueue processed files for selected storage integration.
- Emit final link after upload completes.
- Persist results so links can be copied into downstream DB workflows.

### 5) State-store backend abstraction
- Queue/event/link persistence must be backend-agnostic.
- `sqlite` for local default.
- `postgres` / `mysql` as first-class alternatives.
- SQL behavior must remain idempotent and restart-safe across drivers.

## 2026 Engineering Standards
- Go toolchain pinned with `go.mod` `toolchain` directive and reproducible builds.
- Single binary runtime for orchestration service + web server.
- Structured JSON logging (`slog`) with correlation IDs.
- OpenTelemetry traces/metrics (local OTLP optional, Prometheus endpoint for localhost).
- Strict config validation at boot (`required`, `format`, `directory existence`, `ffmpeg probe`, backend connectivity).
- Idempotent queue design with persistent state (state-store migrations).
- Secure-by-default localhost posture:
- bind only `127.0.0.1`.
- API key/session token for state-changing requests.
- CSRF protection for form endpoints.
- Graceful shutdown with context cancellation and worker drain.

## Proposed Architecture (Single Process, Multi-Worker)
- **Core daemon**: manages schedulers, queue workers, API clients, persistence.
- **CLI layer**: interactive terminal app + command subcommands.
- **Web layer**: localhost dashboard + action endpoints + SSE stream.
- **VPN guard**: periodic safety evaluator with hard-fail semantics.
- **Torrent integration layer**: provider registry and adapter contracts.
- **Storage integration layer**: uploader registry and adapter contracts.
- **State-store layer**: SQL backend abstraction (`sqlite|postgres|mysql`).
- **Pipeline**:
- DownloadComplete -> ConversionQueue -> UploadQueue -> FinalLinkEvent.

## Proposed `maxwell/` Layout
```text
maxwell/
  cmd/maxwell/main.go
  internal/
    app/                # bootstrapping, dependency wiring
    config/             # env + file config schema and validation
    vpn/                # detector and policy engine
    torrent/            # provider contracts + adapters
    queue/              # persistent jobs, retries, idempotency
    convert/            # ffmpeg command builder + runner
    storage/            # upload adapters + link emission
    web/                # HTTP handlers, templates, SSE
    cli/                # interactive CLI commands/views
    events/             # internal pub/sub (download done, converted, uploaded)
    model/              # domain models and enums
  migrations/
    sqlite/
    postgres/
    mysql/
  test/
    integration/
    e2e/
  Makefile
  README.md
```

## Detailed Step Plan (Closure-Oriented)

### Step 1: Bootstrap + strict config contract
Deliverables:
- Typed config for vpn, torrent, storage, ffmpeg, paths, security, workers, state_store.
- Validation for required fields and provider/backend compatibility.
- `maxwell doctor` with pass/fail diagnostics for:
- state-store connectivity.
- torrent API reachability.
- ffmpeg/ffprobe executability.
- download/processed dir writability.

Exit criteria:
- `maxwell doctor` returns actionable diagnostics with non-zero exit on failures.

### Step 2: VPN + home-network policy engine
Implementation details:
- Multi-signal detection:
1. route/interface signal.
2. local network signal (SSID/CIDR).
3. public IP/ASN signal.
- Policy:
- `SAFE`: VPN confirmed and no leak signal.
- `UNSAFE`: no VPN confirmation while default route is active or home signal indicates leak risk.
- `UNKNOWN`: ambiguous signals; treated as unsafe by enforcement.

Exit criteria:
- Unit tests for policy matrix.
- `maxwell vpn status --verbose` explains each signal.

### Step 3: Torrent integration + enforcement hooks
Implementation details:
- Provider selection at runtime.
- Operations:
- list active torrents.
- add magnet with forced save path/category/tags (when provider supports).
- pause/resume all.
- pause/resume specific hashes (when provider supports).
- Enforcement watchdog:
- on `UNSAFE`/`UNKNOWN`, pause-all and block new starts.
- persist reason to event log; surface in UI and CLI.

Exit criteria:
- Integration tests with mocked provider APIs.

### Step 4: State-store abstraction + idempotent queue layer
Implementation details:
- Backend drivers: sqlite, postgres, mysql.
- Tables:
- downloads, conversion_jobs, upload_jobs, links, events.
- Retry scheduling fields:
- attempts, max_attempts, next_attempt_at.
- Idempotency keys:
- conversion by input+preset.
- upload by file identity + destination key.

Exit criteria:
- Restart-safe queue recovery verified on sqlite and one network SQL backend.

### Step 5: Download completion detection + queue handoff
Implementation details:
- Poll provider list/sync endpoint on interval.
- Transition detection to completed state.
- Transactional conversion enqueue with dedupe.

Exit criteria:
- Exactly one conversion job per intended completed video.

### Step 6: FFmpeg conversion engine
Implementation details:
- Preset-driven profiles.
- ffprobe preflight validation.
- stderr/progress capture and persistence.
- Worker concurrency + retries with exponential backoff.

Exit criteria:
- Job transitions: queued -> running -> done|failed.

### Step 7: Upload engine + final link emission
Implementation details:
- Adapter-driven upload with retry/backoff.
- Deterministic object key strategy.
- Link modes:
- public URL.
- signed URL with TTL when integration supports it.
- Persist final_url and emit event stream record.

Exit criteria:
- Immutable completion record with final_url.

### Step 8: CLI UX
Implementation details:
- Commands:
- `maxwell run`
- `maxwell vpn status --verbose`
- `maxwell torrents list`
- `maxwell torrents add <magnet>`
- `maxwell queue list`
- `maxwell links list --latest`
- `maxwell web`
- `maxwell doctor`
- Optional continuous run mode with graceful shutdown.

Exit criteria:
- Full workflow operable from CLI.
- CLI parity tests verify equivalent behavior with web action surface.

### Step 9: Localhost web dashboard
Implementation details:
- Bind `127.0.0.1:<port>`.
- Overview + torrents + queue + links + events views.
- Web actions include magnet add and run-cycle controls equivalent to CLI capabilities.
- Real-time updates over SSE.
- State-changing endpoints require auth token + CSRF token.

Exit criteria:
- Full browser workflow without refresh-dependent status.
- Web/CLI parity integration tests pass for shared actions.

### Step 10: Safety hardening
Implementation details:
- Unsafe/unknown states block magnet starts.
- Mandatory pause-all while unsafe.
- Optional startup `--require-safe-vpn`.
- Debounce state flips to prevent pause/resume thrash.

Exit criteria:
- Unsafe mode consistently blocks starts and maintains pause.

### Step 11: Test strategy and CI
Implementation details:
- Unit tests:
- VPN policy matrix.
- queue dedupe/idempotency.
- retry/backoff scheduler.
- Integration tests:
- each torrent adapter.
- each storage adapter.
- state-store backend selection and migrations.
- ffmpeg invocation harness.
- Web and CLI contract tests.
- E2E smoke:
- add magnet -> simulate completion -> convert -> upload -> final link.

Exit criteria:
- deterministic tests pass with no flaky external dependencies.

### Step 12: Operational readiness and docs
Deliverables:
- README quickstart + security model.
- docs/OPERATIONS.md failure recovery + queue replay.
- docs/CONFIG.md all variables and backend/provider examples.

Exit criteria:
- predictable first-run and troubleshooting workflow.

## Suggested Config Schema (Current Direction)
```yaml
vpn:
  mode: enforce
  check_interval_seconds: 8
  allowed_tunnel_if_prefixes: [utun, tun, wg, ppp]
  public_ip_check_urls:
    - https://api.ipify.org?format=json
    - https://ipinfo.io/json
  allowed_vpn_asns: []
  home_ssids: ["HOME_WIFI_NAME"]
  home_cidrs: ["192.168.1.0/24"]
  home_asns: []
  home_public_ips: []

state_store:
  driver: sqlite              # sqlite | postgres | mysql
  dsn: "./maxwell.db"        # sqlite file or SQL DSN
  max_open_conns: 4

torrent:
  provider: qbittorrent       # qbittorrent | transmission | utorrent
  base_url: "http://127.0.0.1:8080"
  username: ""
  password: ""
  download_dir: "/path/to/downloads"
  category: "maxwell"

ffmpeg:
  bin: "ffmpeg"
  ffprobe_bin: "ffprobe"
  preset: "h264_1080p_fast"

storage:
  provider: backblaze_b2      # backblaze_b2 | aws_s3 | google_drive | onedrive
  endpoint: "https://s3.us-west-000.backblazeb2.com"
  bucket: ""
  region: "us-west-000"
  key_id: ""
  app_key: ""
  access_token: ""
  public_base_url: ""

paths:
  downloads_dir: "/path/to/downloads"
  processed_dir: "/path/to/processed"

workers:
  conversion: 1
  upload: 1
  max_attempts: 5
  backoff_seconds: 5

security:
  web_bind: "127.0.0.1:7777"
  web_token: ""
  csrf_enabled: true
```

## Definition of Done
- Unsafe VPN/home-network state always pauses torrents and blocks new starts.
- Active torrents visible in both CLI and web UI.
- Magnet add works and respects safe policy.
- Completed downloads auto-flow into conversion then upload queues.
- Final uploaded link is recorded and visible/exportable.
- Restart does not lose queue/job/link state.
- State-store backend can be swapped via config without changing business logic.
