# Contributing

Use a current supported Go release. Run `go test -race ./...`, `go vet ./...`
and `gofmt` before proposing changes. Tests use mock miners and temporary
databases. Never commit real credentials, wallet/worker identifiers, device
exports, logs, databases, backups or encryption keys. Use loopback or reserved
documentation addresses in examples and fixtures.

Document the installed firmware/API version for a new device adapter. Keep
monitoring and controls separate, expose only verified control contracts and
preserve unsupported/unknown data without inventing measurements. Device
changes must require an explicit user action.

CI tests Linux, macOS and Windows on amd64 and arm64, runs the race detector
where Go supports it, launches native executables, and builds all release
archives. Python and Bash are build/test tools only, not app dependencies.

## Releases

1. Update `RELEASE_NOTES.md` and any changed compatibility documentation.
2. Push the reviewed commit to `main` and wait for CI to pass.
3. Create and push an annotated `vMAJOR.MINOR.PATCH` tag at that commit.
4. The Release workflow reruns checks for the tagged source, packages binaries
   with licenses/notices and checksums, and publishes the GitHub release only
   after all required jobs pass.

To retry a failed release without moving its tag, rerun its failed jobs or
dispatch the Release workflow at that tag. Publication is staged as a draft;
failed uploads stay draft. The workflow refuses to overwrite a published
release. Fix application code with a new patch version.

For local packaging, set `MINER_FLEET_VERSION=1.0.0` when running
`scripts/build-release.sh dist/bin`, then run
`python3 scripts/package-release.py dist/bin dist/release 1.0.0`.
