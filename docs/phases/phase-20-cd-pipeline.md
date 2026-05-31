---
phase: "20"
title: "CD Pipeline & Multi-Arch Packaging (GoReleaser + GHCR)"
status: completed
progress: 100
depends_on: ["19"]
updated: "2026-05-31"
---

# Phase 20 — CD Pipeline & Multi-Arch Packaging (GoReleaser + GHCR)

> **Depends on**: [Phase 19](phase-19-ci-pipeline.md) (CI Pipeline — ensures tests pass before release)
> **Unblocks**: [Phase 11](phase-11-production-deployment.md) (Production Deployment — needs published images)
> **Status**: Completed

## Goal

Package and publish multi-architecture Docker images via GoReleaser using `dockers_v2` on version-tagged releases (`v*`) and push them to GHCR (GitHub Container Registry).

## Background

The project has five distinct Go binaries (`limen`, `limenctl`, `limen-gateway`, `limen-portal`, `limen-staff`) from the binary split in Phase 9a. Each binary needs a deployable artifact. GoReleaser provides a unified release pipeline that:

- Cross-compiles Go binaries for `linux/amd64` and `linux/arm64`
- Builds lightweight Docker images for each binary
- Publishes multi-arch manifest lists to GHCR
- Cuts GitHub releases with changelogs and checksums

This enables production deployments (Phase 11) to pull from GHCR rather than building locally.

## Design

### Dockerfiles

Five lightweight, GoReleaser-compatible Alpine-based Dockerfiles under `deploy/docker/`. Each copies only its pre-built Go binary — the frontend SPAs are served externally (via reverse proxy or Cloudflare Pages), so no static assets are included.

```dockerfile
# deploy/docker/Dockerfile.limen (example)
FROM docker.io/library/alpine:3.21
RUN apk --no-cache add ca-certificates
COPY limen /usr/local/bin/limen
ENTRYPOINT ["/usr/local/bin/limen"]
```

The same pattern applies to all five: `limenctl`, `limen-gateway`, `limen-portal`, `limen-staff`.

### GoReleaser Configuration

`.goreleaser.yaml` in the workspace root with:

- **5 build targets**: one per binary, each producing `linux/amd64` and `linux/arm64` output
- **`dockers_v2` section** (experimental but recommended for multi-arch manifest lists):
  - Each Dockerfile gets two image entries (`ghcr.io/<owner>/limen:{{ .Tag }}-amd64`, `ghcr.io/<owner>/limen:{{ .Tag }}-arm64`) with `use: buildx`
  - GoReleaser builds both arch-specific images and creates the manifest list automatically
  - Tags: `{{ .Tag }}`, `{{ .Major }}`, `latest` (on stable releases)

```
.goreleaser.yaml
  ├── builds:
  │   ├── limen
  │   ├── limenctl
  │   ├── limen-gateway
  │   ├── limen-portal
  │   └── limen-staff
  └── dockers_v2:
      ├── limen       (linux/amd64 + linux/arm64 → GHCR manifest)
      ├── limenctl    (linux/amd64 + linux/arm64 → GHCR manifest)
      ├── limen-gateway
      ├── limen-portal
      └── limen-staff
```

### Release Workflow

`.github/workflows/release.yml` triggered on push of tags matching `v*`:

```
.github/workflows/release.yml
  ├── on: push.tags = ['v*']
  ├── jobs:
  │   ├── release:
  │       ├── qemu-setup              (docker/setup-qemu-action)
  │       ├── buildx-setup            (docker/setup-buildx-action)
  │       ├── ghcr-login              (docker/login-action → ghcr.io)
  │       └── goreleaser              (goreleaser/goreleaser-action)
```

Steps:
1. **QEMU setup** — `docker/setup-qemu-action@v3` enables multi-platform emulation for Alpine builds targeting arm64 on amd64 runners.
2. **Buildx setup** — `docker/setup-buildx-action@v3` creates a builder with `linux/amd64,linux/arm64` platforms.
3. **GHCR login** — `docker/login-action@v3` authenticates to `ghcr.io` using `GITHUB_TOKEN`.
4. **GoReleaser** — `goreleaser/goreleaser-action@v6` runs `goreleaser release --clean`, which reads `.goreleaser.yaml`, compiles binaries, builds Docker images, pushes manifests to GHCR, and creates a GitHub release.

### Image Naming

Images published to GHCR follow the convention:

```
ghcr.io/<owner>/limen:<tag>
ghcr.io/<owner>/limenctl:<tag>
ghcr.io/<owner>/limen-gateway:<tag>
ghcr.io/<owner>/limen-portal:<tag>
ghcr.io/<owner>/limen-staff:<tag>
```

Tag format: `v1.2.3` (from git tag), plus `latest` on stable releases.

## Deliverables

| File | Change |
|------|--------|
| `deploy/docker/Dockerfile.limen` | New file: Alpine image copying `limen` binary |
| `deploy/docker/Dockerfile.limenctl` | New file: Alpine image copying `limenctl` binary |
| `deploy/docker/Dockerfile.limen-gateway` | New file: Alpine image copying `limen-gateway` binary |
| `deploy/docker/Dockerfile.limen-portal` | New file: Alpine image copying `limen-portal` binary |
| `deploy/docker/Dockerfile.limen-staff` | New file: Alpine image copying `limen-staff` binary |
| `.goreleaser.yaml` | New file: 5 build targets + `dockers_v2` multi-arch definitions |
| `.github/workflows/release.yml` | New file: tag-triggered CD pipeline |
| `docs/phases/phase-20-cd-pipeline.md` | This file |
| `docs/phases/README.md` | Updated index |

## Risks

- **Docker buildx emulation cross-arch**: Building `linux/arm64` images on an `ubuntu-latest` (amd64) runner requires QEMU emulation. Alpine builds are typically fast, but arm64 emulation can be slow for Go binaries if the build step does more than just `COPY`. Since these Dockerfiles are binary-only copies, the actual Go cross-compilation happens in GoReleaser's build step (native Go cross-compilation, no emulation needed for compile). The `buildx` build step is lightweight.
- **GHCR permissions**: The workflow must use `permissions: packages: write` for the push to succeed. This is standard for repos owned by the publishing org/account.
- **GoReleaser version**: Using `dockers_v2` instead of the stable `dockers` directive. Check GoReleaser release notes for stability of this feature. If `dockers_v2` is not yet production-ready, fall back to the `dockers` directive with manual manifest publishing.
- **Tag format**: Only tags matching `v*` trigger the release workflow. Ensure existing tags and future versioning conventions align.

## Checklist

- [x] Create `deploy/docker/Dockerfile.limen` copying the `limen` binary
- [x] Create `deploy/docker/Dockerfile.limenctl` copying the `limenctl` binary
- [x] Create `deploy/docker/Dockerfile.limen-gateway` copying the `limen-gateway` binary
- [x] Create `deploy/docker/Dockerfile.limen-portal` copying the `limen-portal` binary
- [x] Create `deploy/docker/Dockerfile.limen-staff` copying the `limen-staff` binary
- [x] Create `.goreleaser.yaml` with the 5 build targets and their `dockers_v2` multi-arch definitions
- [x] Create `.github/workflows/release.yml` with QEMU, Buildx, GHCR login, and GoReleaser action steps
- [x] Validate GoReleaser local build failure modes and outputs via `goreleaser release --snapshot --skip=publish --clean`
