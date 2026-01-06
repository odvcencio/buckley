# Buckley Release Process

This guide documents how to create releases for Buckley.

## Version Scheme

Buckley follows [Semantic Versioning](https://semver.org/):

```
MAJOR.MINOR.PATCH
```

- **MAJOR**: Breaking changes to CLI, config, or API
- **MINOR**: New features, backward-compatible
- **PATCH**: Bug fixes, backward-compatible

Pre-release versions use suffixes: `1.0.0-alpha`, `1.0.0-beta.1`, `1.0.0-rc.1`

## Release Checklist

### Pre-Release

- [ ] All tests pass: `./scripts/test.sh`
- [ ] Tests pass with race detector: `GO_TEST_RACE=1 ./scripts/test.sh`
- [ ] No lint issues: `golangci-lint run`
- [ ] Documentation is current
- [ ] CHANGELOG.md updated with release notes
- [ ] Version in code matches tag (if applicable)

### Creating a Release

#### 1. Update CHANGELOG.md

Move items from `[Unreleased]` to a new version section:

```markdown
## [Unreleased]

## [1.2.0] - 2024-01-15

### Added
- New feature X (#123)

### Changed
- Improved Y behavior (#124)

### Fixed
- Bug in Z (#125)
```

#### 2. Create and Push Tag

```bash
# Ensure you're on main and up to date
git checkout main
git pull origin main

# Create annotated tag
git tag -a v1.2.0 -m "Release v1.2.0"

# Push tag
git push origin v1.2.0
```

#### 3. Automated Release

GitHub Actions automatically:
1. Runs full test suite
2. Builds binaries for all platforms
3. Creates GitHub Release with:
   - Compiled binaries (Linux, macOS, Windows)
   - Checksums file
   - Auto-generated changelog

### Release Artifacts

Each release produces:

| Artifact | Platforms |
|----------|-----------|
| `buckley_linux_amd64.tar.gz` | Linux x86_64 |
| `buckley_linux_arm64.tar.gz` | Linux ARM64 |
| `buckley_darwin_amd64.tar.gz` | macOS Intel |
| `buckley_darwin_arm64.tar.gz` | macOS Apple Silicon |
| `buckley_windows_amd64.zip` | Windows x86_64 |
| `checksums.txt` | SHA256 checksums |

## Build Configuration

### goreleaser.yaml

The release is configured via `.goreleaser.yaml`:

```yaml
version: 2
builds:
  - main: ./cmd/buckley
    binary: buckley
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ignore:
      - goos: windows
        goarch: arm64
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.buildDate={{.Date}}
```

### Build Metadata

Binaries include:
- **Version**: From git tag
- **Commit**: Full git SHA
- **Build Date**: ISO 8601 timestamp

Query with: `buckley --version`

### CGO

CGO is disabled (`CGO_ENABLED=0`) for:
- Maximum portability
- Static binaries
- No external dependencies

This uses pure-Go SQLite (`modernc.org/sqlite`) which is slower than CGO but fully portable.

## Manual Release Build

For local testing or custom builds:

```bash
# Install goreleaser
go install github.com/goreleaser/goreleaser@latest

# Build snapshot (no tag required)
goreleaser build --snapshot --clean

# Full release (requires tag)
goreleaser release --clean
```

## Verifying Releases

### Checksum Verification

```bash
# Download release and checksums
curl -LO https://github.com/odvcencio/buckley/releases/download/v1.2.0/buckley_linux_amd64.tar.gz
curl -LO https://github.com/odvcencio/buckley/releases/download/v1.2.0/checksums.txt

# Verify
sha256sum -c checksums.txt --ignore-missing
```

### Signature Verification

(Future: GPG signatures planned for v1.1+)

## Docker Images

Runtime images are published via GoReleaser on tagged releases (see `.goreleaser.yaml` `dockers_v2`):

```bash
# Pull official image
docker pull ghcr.io/odvcencio/buckley:latest
docker pull ghcr.io/odvcencio/buckley:v1.2.0

# Run Mission Control (default CMD in the image)
docker run --rm \
  -p 4488:4488 \
  -e BUCKLEY_IPC_TOKEN="REPLACE_ME" \
  ghcr.io/odvcencio/buckley:latest

# Run the CLI instead of the server (override CMD)
docker run --rm -it \
  -e OPENROUTER_API_KEY \
  -v "$(pwd):/workspace" \
  ghcr.io/odvcencio/buckley:latest --help
```

Development tooling images (golang/node) are built via `.github/workflows/docker-images.yml`:

- `ghcr.io/odvcencio/buckley/buckley-go`
- `ghcr.io/odvcencio/buckley/buckley-node`

## Self-Hosted Upgrades (DB + Artifacts)

Buckley stores state in SQLite plus on-disk artifacts (plans/execution/reviews). Before upgrading a self-hosted instance, take a backup of both.

### Backup

1. Back up the SQLite database (online snapshot via `VACUUM INTO`):
   ```bash
   buckley db backup --out /path/to/backups/buckley.db
   ```
2. Back up artifacts and checkpoints by copying the configured directories (see `artifacts.*` in `docs/CONFIGURATION.md` and `BUCKLEY_DB_PATH` / `BUCKLEY_DATA_DIR`).

For the Helm chart defaults:
- DB: `/buckley/projects/.buckley/buckley.db`
- Artifacts: `/buckley/shared/artifacts/`

### Upgrade

- Apply migrations before starting the new version:
  ```bash
  buckley migrate
  ```

The Helm chart runs a pre-upgrade migration job when `migrationsJob.enabled=true`.

### Restore (Rollback)

1. Stop the Buckley server.
2. Restore the DB from backup:
   ```bash
   buckley db restore --in /path/to/backups/buckley.db --force
   ```
3. Restore artifacts by copying your backup back into place.

## Hotfix Process

For critical fixes to released versions:

```bash
# Create hotfix branch from tag
git checkout -b hotfix/v1.2.1 v1.2.0

# Apply fix
# ... make changes ...

# Update CHANGELOG
# Add entry under new version

# Commit and tag
git commit -am "fix: critical bug in X"
git tag -a v1.2.1 -m "Hotfix v1.2.1"

# Push
git push origin hotfix/v1.2.1
git push origin v1.2.1
```

## Beta/RC Releases

For pre-release testing:

```bash
# Beta release
git tag -a v1.3.0-beta.1 -m "Beta release v1.3.0-beta.1"
git push origin v1.3.0-beta.1

# Release candidate
git tag -a v1.3.0-rc.1 -m "Release candidate v1.3.0-rc.1"
git push origin v1.3.0-rc.1
```

Pre-releases are marked as such on GitHub and not shown as "latest".

## Rollback

If a release has critical issues:

1. **Delete the release** on GitHub (keeps tag)
2. **Delete the tag** if needed:
   ```bash
   git tag -d v1.2.0
   git push origin :refs/tags/v1.2.0
   ```
3. **Create hotfix** following process above

## Release Notes Template

```markdown
## What's Changed

### ✨ New Features
- Feature description (#PR)

### 🐛 Bug Fixes
- Fix description (#PR)

### 📚 Documentation
- Doc update (#PR)

### 🔧 Internal
- Refactoring/cleanup (#PR)

## Breaking Changes

- Description of breaking change and migration path

## Upgrade Notes

Brief notes for users upgrading from previous version.

## Contributors

Thanks to @contributor1, @contributor2 for their contributions!
```

## CI/CD Pipeline

### Test Workflow (ci.yml)

Runs on every push/PR:
- Unit tests
- Race detector tests
- Linting (gofmt, go vet, golangci-lint)
- Multi-platform builds

### Release Workflow (release.yml)

Runs on tag push:
1. Checkout code
2. Setup Go
3. Run goreleaser
4. Upload to GitHub Releases

### Docker Workflow (docker-images.yml)

Runs on:
- Push to main (`:latest` tag)
- Tag push (`:vX.Y.Z` tag)

## Monitoring Releases

After release:
1. Check [GitHub Actions](https://github.com/odvcencio/buckley/actions) for success
2. Verify [Releases page](https://github.com/odvcencio/buckley/releases) shows artifacts
3. Test download and checksum
4. Announce in relevant channels

## See Also

- [CLI Reference](CLI.md)
- [Configuration Reference](CONFIGURATION.md)
- [Contributing Guide](../CONTRIBUTING.md)
