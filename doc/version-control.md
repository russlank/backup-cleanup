# Version control and release workflow

This document explains the Git branching model, commit conventions, tagging strategy, and the end-to-end process for publishing a new release.

## Table of contents

1. [Repository](#1-repository)
2. [Branch model](#2-branch-model)
3. [Commit message convention](#3-commit-message-convention)
4. [Versioning scheme](#4-versioning-scheme)
5. [Day-to-day development workflow](#5-day-to-day-development-workflow)
6. [Publishing a release](#6-publishing-a-release)
7. [Hotfix workflow](#7-hotfix-workflow)
8. [How version information is embedded in the binary](#8-how-version-information-is-embedded-in-the-binary)
9. [Working with tags](#9-working-with-tags)

---

## 1. Repository

| Property | Value |
|---|---|
| Remote URL | `https://github.com/russlank/backup-cleanup` |
| Default branch | `main` |
| Development branch | `develop` |
| CI | Woodpecker (see [doc/ci-pipeline.md](ci-pipeline.md)) |

---

## 2. Branch model

```
main        ──●──────────────────●──── (stable; only tagged commits)
               \                /
develop         ●───●───●───●──      (integration branch)
                         \
feature/xyz               ●───●      (short-lived feature/fix branches)
```

### `main`

- Always reflects a production-ready state.
- Only receives merges from `develop` (or a hotfix branch).
- Every commit on `main` is tagged with a semver tag that triggers a CI release.
- **Never commit directly to `main`.**

### `develop`

- The primary integration branch.
- All feature branches are merged here first.
- The CI pipeline runs lint, vet, test, and build on every push to `develop`.
- When `develop` is stable and ready to release, it is merged into `main` and tagged.

### Feature / fix branches

- Short-lived branches created from `develop`.
- Named descriptively: `feature/add-s3-support`, `fix/weekly-anchor-off-by-one`.
- Deleted after merging.

---

## 3. Commit message convention

Follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.  This is not enforced by a hook, but it makes the git log easy to read and enables automated changelog generation in the future.

```
<type>(<scope>): <short description>

[optional body]

[optional footer]
```

### Common types

| Type | When to use |
|---|---|
| `feat` | A new feature or capability |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `test` | Adding or modifying tests |
| `refactor` | Code change that is neither a feature nor a fix |
| `ci` | Changes to the CI pipeline |
| `chore` | Dependency updates, build scripts, etc. |

### Examples

```
feat: add --version flag to binary
fix: prevent integer overflow in daily cutoff calculation
docs: add architecture and testing guide
test: add cleanupFullBackups monthly retention tests
ci: fix Woodpecker variable substitution in release step
```

### Breaking changes

If a change is not backward compatible, add `!` after the type or include `BREAKING CHANGE:` in the footer:

```
fix!: reject negative retention values (previously accepted silently)
```

---

## 4. Versioning scheme

This project uses **Semantic Versioning** ([semver.org](https://semver.org)):

```
v<MAJOR>.<MINOR>.<PATCH>
```

| Segment | Increment when |
|---|---|
| `MAJOR` | Backward-incompatible change (e.g. removed CLI flag, changed config variable name) |
| `MINOR` | New backward-compatible feature |
| `PATCH` | Bug fix that does not change the external interface |

### Pre-release tags

Append a hyphen and a label to the tag:

```
v1.2.0-rc.1    # release candidate
v1.2.0-beta.1  # beta
v1.2.0-alpha.1 # alpha
```

The CI pipeline detects the hyphen and marks the Gitea release as a pre-release.

### Initial releases

The first public release was `v0.0.1`.  The `v0.x.y` range signals that the public API may still change.  Increment to `v1.0.0` when the binary is considered stable for production deployments.

---

## 5. Day-to-day development workflow

### Starting new work

```bash
# Make sure develop is up to date
git checkout develop
git pull

# Create a feature branch
git checkout -b feature/my-feature
```

### Making changes

```bash
# Edit files, then test locally
go test -count=1 ./...
make fmt

# Commit
git add -p                         # review each change
git commit -m "feat: my new feature"
```

### Pushing and creating a pull request

```bash
git push --set-upstream origin feature/my-feature
```

Open a pull request on Gitea from `feature/my-feature` → `develop`.  The CI pipeline will run lint, vet, test, and build automatically.

### Merging

After the pipeline passes and the pull request is reviewed:

1. Merge the pull request on Gitea (prefer "merge commit" or "squash merge").
2. Delete the feature branch.

---

## 6. Publishing a release

### Step 1 — Merge develop into main

```bash
git checkout main
git pull
git merge --no-ff develop
```

The `--no-ff` flag creates an explicit merge commit, which makes the history easier to read.

### Step 2 — Create and push the tag

```bash
# Replace 1.2.3 with the actual version
git tag v1.2.3
git push origin main
git push origin v1.2.3
```

Or in one step:

```bash
git push origin main && git push origin v1.2.3
```

### Step 3 — Watch the CI pipeline

Open the Woodpecker dashboard and confirm that:

1. `lint` passes
2. `vet` passes
3. `test` passes
4. `build` passes (produces `dist/backup-cleanup-linux-amd64`, etc.)
5. `release` passes (uploads binaries to Gitea Releases)

### Step 4 — Verify the release

```
https://github.com/russlank/backup-cleanup/releases
```

Confirm that:
- The tag `v1.2.3` appears.
- Three assets are attached: `backup-cleanup-linux-amd64`, `backup-cleanup-linux-arm64`, `SHA256SUMS`.
- The binary prints the correct version:

```bash
./backup-cleanup-linux-amd64 --version
# backup-cleanup v1.2.3 (commit <sha>, built <date>)
```

### Step 5 — Bump develop

After the release, merge `main` back into `develop` to keep the branch history clean:

```bash
git checkout develop
git merge main
git push
```

---

## 7. Hotfix workflow

A hotfix is a critical bug fix that must go directly to production without waiting for the normal `develop → main` cycle.

```bash
# Branch from main, not develop
git checkout main
git pull
git checkout -b hotfix/fix-description

# Make the fix and test it
go test -count=1 ./...
git commit -m "fix: critical description"

# Merge back to main and tag
git checkout main
git merge --no-ff hotfix/fix-description
git tag v1.2.4
git push origin main v1.2.4

# Also merge back to develop so the fix is not lost
git checkout develop
git merge hotfix/fix-description
git push

# Clean up
git branch -d hotfix/fix-description
```

---

## 8. How version information is embedded in the binary

The version string is injected by the Go linker (`-X` flag) at build time.  Three values are embedded:

| Variable in Go | Linker flag | Source at build time |
|---|---|---|
| `main.version` | `-X main.version=$(VERSION)` | Git tag (e.g. `v1.2.3`) |
| `main.commit` | `-X main.commit=$(COMMIT)` | Full git commit SHA |
| `main.buildDate` | `-X main.buildDate=$(DATE)` | UTC timestamp |

### In the Makefile

```makefile
VERSION ?= dev
COMMIT  ?= unknown
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
    -X main.version=$(VERSION) \
    -X main.commit=$(COMMIT) \
    -X main.buildDate=$(DATE)
```

To build a specifically versioned binary locally:

```bash
make VERSION=v1.2.3 COMMIT=$(git rev-parse HEAD) dist
./dist/backup-cleanup-linux-amd64 --version
```

### In the CI pipeline

The `build` step passes all three values to `make`:

```sh
VERSION="${CI_COMMIT_TAG:-dev}"
COMMIT="${CI_COMMIT_SHA:-unknown}"
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
make VERSION="$VERSION" COMMIT="$COMMIT" DATE="$DATE" dist
```

On a tag build, `CI_COMMIT_TAG` is the tag name (e.g. `v1.2.3`); on a regular push, it is empty and the `:-dev` fallback applies.

---

## 9. Working with tags

### List all tags

```bash
git tag -l
```

### List tags with their commit messages

```bash
git tag -l -n1
```

### Delete a tag locally and on the remote

Only do this if the tag was pushed by mistake and the CI release has not yet been published:

```bash
# Delete locally
git tag -d v1.2.3

# Delete on remote
git push origin :refs/tags/v1.2.3
```

If the CI pipeline already created a Gitea release for the tag, delete the release in the Gitea UI first (`Releases → v1.2.3 → Delete`), then delete the tag.

### Re-tag a commit

If you tagged the wrong commit:

```bash
git tag -d v1.2.3
git push origin :refs/tags/v1.2.3
git tag v1.2.3 <correct-commit-sha>
git push origin v1.2.3
```

### Verify a release binary checksum

After downloading from Gitea Releases:

```bash
sha256sum -c SHA256SUMS
```
