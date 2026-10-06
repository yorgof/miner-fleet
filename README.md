# Miner Fleet

A local web dashboard for Bitaxe, NerdQAxe, CYD, Antminer S9 running
BraiinsOS+, and Avalon Nano 3 miners. Go embeds the HTML, CSS, JavaScript,
timezone database and SQLite driver into one executable. No Docker, Node,
Python, external database or cloud account is required to run it.

## Run on a desktop

Start the executable and open <http://127.0.0.1:8080>. Add your miners using
**Add miner**. The app starts with no registered devices or example data.
Keep it running for polling, alerts and schedules to work.

By default it listens only on your desktop's loopback interface and saves
state in your operating system's user configuration folder:

- Linux: `$XDG_CONFIG_HOME/miner-fleet`, or `~/.config/miner-fleet`.
- macOS: `~/Library/Application Support/miner-fleet`.
- Windows: `%AppData%\miner-fleet`.

To run from source (Go 1.25 or newer):

```sh
git clone https://github.com/yorgof/miner-fleet.git
cd miner-fleet
go run .
```

To build a native executable:

```sh
CGO_ENABLED=0 go build -trimpath -o miner-fleet .
```

On Windows, set `CGO_ENABLED=0` in your shell before `go build`.
`scripts/build-release.sh` builds Linux, macOS and Windows binaries for
amd64 and arm64. An optional argument selects the output directory;
default `dist/` is ignored by Git. Cross-compilation is checked; native
launch testing on macOS and Windows remains a release task. Distribute the
[third-party notices](THIRD_PARTY_NOTICES.md) with binaries. The project
license still needs to be selected before a public release.

## Run with Docker

The Docker image includes the same executable and listens on container port
8080. Mount a persistent directory for the database and its encryption key:

```sh
docker build -t miner-fleet .
mkdir -p data
docker run --rm --name miner-fleet --user "$(id -u):$(id -g)" \
  -p 127.0.0.1:8080:8080 -v "$PWD/data:/data" miner-fleet
```

This example uses your current user to write the mounted directory. Without
`--user`, the image runs as UID/GID 65532 and needs a directory writable by
that identity. Pass flags after the image name to override its default
`-addr=:8080 -db=/data/miner-fleet.db`. Native executables retain the desktop
defaults above. Back up the database and matching key before replacing a
container.

## Dashboard and miner details

Cards show reachability, mining/pause status, firmware/model, hashrate,
shares and rejection percentage, best difficulty, uptime, signal strength,
reported power, efficiency, temperatures, fan readings and ASIC settings
when available. S9 power is an estimate. Missing sensors are not inferred.

The details page groups every collected field, including nested arrays of
boards, chips, fans, pools, tuning and configuration. Filter by field name
or value, download redacted telemetry JSON, and view 24-hour charts.
Passwords, tokens and other authentication fields are redacted before
new telemetry is stored or rendered. Firmware log downloads are private
device diagnostics and can contain identifying information.

Forms stay outside the periodic telemetry refresh, preserving edits.
Changes require an explicit button press and confirmation. Saving a profile
does not apply it. Firmware uploads require an ESP OTA image for the exact
board; factory images and arbitrary update URLs are not accepted.

| Device | Connection | Data and controls |
| --- | --- | --- |
| Bitaxe / AxeOS | HTTP, default 80 | System/ASIC/scoreboard data; frequency/voltage, fan control, primary/backup pools including TLS and V2 settings, network/display/advanced settings, identify, restart, block-notification dismissal, logs, Wi-Fi scan and OTA uploads. Pause/resume appears when firmware reports that capability. |
| NerdQAxe / AxeOS fork | HTTP, default 80 | Per-ASIC/per-fan/PID data, auxiliary reporting/notification/swarm/authentication/update status; performance, global and individual fans, pools, network/display, InfluxDB and device alerts, restart, stop hashing, reset statistics, five-second WebSocket log capture and OTA uploads. Restart resumes a stopped miner. |
| CYD miner | Its screen's IPv4 address, HTTP 80 | Firmware, hashrate, heap/SHA/network/pool/BTC data; all firmware settings including pool/worker/payout, Wi-Fi, currency/screen flip and web password; restart, Wi-Fi scan and OTA. No fan, temperature, power, pause or brightness API exists in the supported firmware. |
| S9 / BraiinsOS+ | TCP 4028; web HTTP 80 | Summary, boards/devices, fans/temperatures, tuner, pools, version, stats/config/coin data; socket pause/resume. Authenticated GraphQL adds persistent configuration, power/hashrate targets, scaling and board tuning, cooling/immersion settings, pool/group management, hostname/network/password, identify, auto-upgrade, mining service start/stop/restart, reboot and logs. |
| Avalon Nano 3 | TCP 4028 | Existing summary/stats data, including complete packed device responses. Monitoring only: no verified write API is integrated. Use Open device interface for its controls. |

Contracts were checked against AxeOS v2.14.1, NerdQAxe v1.0.37.3-LTS,
CYD v1.0.0 and the S9's BraiinsOS+ 22.08.1 GraphQL schema. Other firmware
versions can differ. Device APIs remain responsible for accepting settings;
a rejected command is reported and recorded. API acceptance does not prove
that persistent changes or a tuning cycle have completed.

## Credentials and access

Enter each device's login in **Connection credentials** on its details page.
For S9, enter its web username/password; monitoring and socket pause/resume
do not need that login. A blank password preserves the saved password.
For NerdQAxe with OTP enabled, enter a current six-digit code to establish
its firmware's 24-hour session. Obtain a new session when it expires;
scheduled changes will fail visibly if authentication has expired.

Credentials and saved profile payloads are encrypted using AES-GCM. The
32-byte key is in `miner-fleet.db.key` beside the database; Unix permissions
are 0600 on both. Encryption protects a database copy that lacks the key.
It does not isolate data from someone who controls the running OS account.
Use HTTPS when accessing the app over the network to enter credentials.
Device HTTP/TCP APIs may themselves be unencrypted on the LAN.

Optional environment variables `MINER_FLEET_PASSWORD` and
`MINER_FLEET_USERNAME` enable dashboard HTTP Basic authentication (username
default `admin`). With no password, access follows the listen address;
keep network listeners on a trusted LAN or VPN. Cross-site browser writes
are rejected. `/healthz` remains available for local container checks.

`MINER_FLEET_CYD_PASSWORD` remains an optional shared fallback for CYDs;
a saved per-device password takes precedence.

## Fleet settings

- Health incidents cover unreachable devices, zero hashrate, known pool
  loss, recent rejected shares, temperature and stalled fans. Persistence
  defaults to three minutes; high temperature is immediate. Recoveries and
  restarts enter the change log. Expected-offline miners can be muted;
  intentional pauses suppress no-hashing and pool alerts.
- Set an electricity rate and currency. Energy uses adjacent successful
  power readings, excluding outages, missing power and gaps longer than
  three polling intervals. Coverage is shown per miner. Values represent
  observed history, with miner-reported estimates labelled explicitly.
- Save named settings profiles, apply them manually, or add paired schedules
  for quiet hours/heater modes. Choose an IANA timezone and weekdays. Enable
  schedules explicitly; they can be toggled off or removed. Each time slot
  is claimed durably before contacting a device, and failures are recorded
  without automatic retries. Missed times are not replayed after shutdown.
  A repeated daylight-saving minute runs once per actual occurrence.
- Export/import configuration for registrations, settings, profiles and
  schedules. Exports omit credentials, profile passwords/tokens, notification
  secrets and history, but include miner addresses and worker/payout IDs.
  Imports add records atomically, retain existing history, and disable all
  imported schedules. Re-enter secret profile fields separately.
- Download a consistent SQLite backup. Preserve the matching `.db.key`
  separately to restore encrypted credentials and profiles. Restore with
  the app stopped. Opening encrypted state without its key fails clearly.

Notifications use ntfy when `MINER_FLEET_NTFY_URL` is set. Optional
`MINER_FLEET_NTFY_TOKEN` authenticates publishing;
`MINER_FLEET_NTFY_EMAIL` requests the provider's email relay. Existing
block/best-difficulty notifications use `MINER_FLEET_DIFF_THRESHOLD`
(default 1e12). Health notifications additionally need their fleet checkbox
enabled. Topic URLs and tokens are private. `-alert-test` sends a test.

## Flags and development

| Flag | Default | Purpose |
| --- | --- | --- |
| `-addr` | `127.0.0.1:8080` | Listen address |
| `-db` | User config folder / `miner-fleet/miner-fleet.db` | Persistent state |
| `-poll-interval` | `15s` | Per-device polling, minimum 1s |
| `-retention` | `2160h` | Sample/event retention, minimum 1h |
| `-healthcheck` | off | GET local `/healthz` and exit |
| `-alert-test` | off | Send a configured ntfy test and exit |

The `internal/axeos`, `cyd`, `braiins`, and `avalon` packages implement device
protocols. `internal/miners` normalizes readings and serializes controls per
device. `internal/fleet` handles health, schedules and energy.
`internal/store` owns migrations, SQLite and encryption. `internal/web`
renders embedded templates and routes. The frontend uses bundled HTMX;
there is no frontend compilation or runtime CDN dependency.

Run `go test -race ./...` and `go vet ./...`. Tests use loopback mock devices
and temporary databases; they never need real miners or credentials.
