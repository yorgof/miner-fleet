# Multi-stage build: compile a static Go binary, ship it on distroless with
# no shell and no package manager. CGO is off deliberately - the SQLite
# driver (modernc.org/sqlite) is pure Go, so nothing here needs gcc/musl-dev.
ARG BUILDER_IMAGE=golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM ${BUILDER_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/miner-fleet .

FROM ${RUNTIME_IMAGE}
# Runs as the image's nonroot user (65532:65532) unless overridden.
# Mount a writable /data directory to persist the database and encryption key.
COPY --from=build /out/miner-fleet /miner-fleet
EXPOSE 8080
ENTRYPOINT ["/miner-fleet"]
CMD ["-addr=:8080", "-db=/data/miner-fleet.db"]
