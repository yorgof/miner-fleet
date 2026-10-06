# Miner Fleet 1.0.0

First standalone MIT-licensed release of Miner Fleet.

- Local dashboard, telemetry history and firmware-specific controls for
  Bitaxe, NerdQAxe, CYD and Antminer S9 running BraiinsOS+.
- Read-only monitoring for Avalon Nano 3.
- Fleet health alerts, electricity estimates, encrypted device credentials,
  settings profiles, schedules, configuration import/export and backups.
- One executable per platform, with embedded web assets, timezone data and
  pure-Go SQLite. No Docker or external database is required.

Download the archive for your OS and CPU architecture, extract it, and start
`miner-fleet` (`miner-fleet.exe` on Windows). Open <http://127.0.0.1:8080>.
`darwin` archives are for macOS. `amd64` means Intel/AMD 64-bit; `arm64`
includes Apple Silicon and 64-bit ARM computers. Each archive includes the
MIT license, third-party notices and README. `SHA256SUMS` covers the six
archives; compare checksums before running a download.

Firmware compatibility is documented in the README. Persistent S9 controls
target the legacy BraiinsOS+ 22.08.1 GraphQL API. Successful API acceptance
does not prove that persistent settings or tuning completed. Avalon writes
are not integrated. Device settings and firmware uploads require explicit
actions; saving a profile does not apply it.

CI tests native executables on all six target OS/architecture combinations.
These are automated startup/UI checks with empty state and mock devices;
interactive desktop installation and every real-device write are not covered.
macOS and Windows downloads are not code signed or notarized.

Back up the database and its matching `.db.key` before upgrading an existing
installation. Network access defaults to loopback; use trusted HTTPS and
optional dashboard authentication when exposing it to other computers.
