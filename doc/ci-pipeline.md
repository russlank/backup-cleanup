# CI pipeline

This document describes the Woodpecker CI pipeline for `backup-cleanup`, the equivalent GitHub Actions workflow, common failure modes, and how to extend either pipeline.

## Table of contents

1. [Infrastructure overview](#1-infrastructure-overview)
2. [Pipeline file location and structure](#2-pipeline-file-location-and-structure)
3. [The five pipeline steps](#3-the-five-pipeline-steps)
4. [How Woodpecker variable substitution works](#4-how-woodpecker-variable-substitution-works)
5. [Secrets](#5-secrets)
6. [Triggering the pipeline](#6-triggering-the-pipeline)
7. [How a release is published](#7-how-a-release-is-published)
8. [Common failures and fixes](#8-common-failures-and-fixes)
9. [Extending the pipeline](#9-extending-the-pipeline)
10. [GitHub Actions equivalent](#10-github-actions-equivalent)

---

## 1. Infrastructure overview

| Component | Value |
|---|---|
| CI system | [Woodpecker CI](https://woodpecker-ci.org) |
| Source repository | `https://git.digixoil.se/digixoil/backup-cleanup` (private Gitea) |
| Binary release storage | Gitea release assets on the same server |
| Go image | `golang:1.26-alpine` (change `&go_image` to upgrade) |
| Release upload image | `alpine:3.20` |

Woodpecker is connected to Gitea via a Gitea OAuth application.  When commits or tags are pushed to Gitea, Gitea sends a webhook to the Woodpecker server, which schedules the pipeline run.

---

## 2. Pipeline file location and structure

```
.woodpecker.yml          ← pipeline definition (project root)
```

High-level structure:

```yaml
variables:
  - &go_image 'golang:1.26-alpine'   # YAML anchor — change here to upgrade Go

steps:
  lint:    { when: [push, tag, pull_request] }
  vet:     { when: [push, tag, pull_request] }
  test:    { when: [push, tag, pull_request] }
  build:   { when: [push, tag, pull_request] }
  release: { when: tag }
```

Steps run sequentially.  If any step fails, subsequent steps are skipped and the pipeline is marked as failed.

---

## 3. The five pipeline steps

### Step 1 — `lint` (format check)

**Image:** `golang:1.26-alpine`  
**When:** push, tag, pull\_request

```sh
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "The following files are not gofmt-formatted:"
  echo "$unformatted"
  echo "Run: make fmt"
  exit 1
fi
```

`gofmt -l .` recursively lists every `.go` file in the working directory whose formatting does not match `gofmt` canonical output.  If the list is non-empty, the step exits with code 1 and the pipeline fails.

**Fix locally:**

```bash
make fmt
# or
gofmt -w .
```

**Why `gofmt -l .` and not `gofmt -l ./...`?**

`./...` is a Go build-tool wildcard understood by commands like `go build` and `go test`.  It is **not** valid for `gofmt`, which only accepts file paths or directory paths.  Passing `.` tells `gofmt` to recurse the current directory, which is equivalent.

### Step 2 — `vet` (static analysis)

**Image:** `golang:1.26-alpine`  
**When:** push, tag, pull\_request

```sh
go vet ./...
```

`go vet` runs a collection of static analysers that catch common mistakes:

- Incorrect `fmt.Printf` format strings
- Unreachable code
- Suspicious composite literals
- Misuse of `sync.Mutex`

`go vet` is included in the Go toolchain and requires no separate installation.

### Step 3 — `test`

**Image:** `golang:1.26-alpine`  
**When:** push, tag, pull\_request

```sh
go test -count=1 ./...
```

Runs the full test suite.  `-count=1` disables the test cache so every CI run re-executes all tests.

The test suite creates temporary files in `os.TempDir()` and cleans them up via `t.TempDir()`.  No external services, databases, or credentials are needed.

### Step 4 — `build`

**Image:** `golang:1.26-alpine`  
**When:** push, tag, pull\_request

```sh
apk add --no-cache make
VERSION="${CI_COMMIT_TAG:-dev}"
COMMIT="${CI_COMMIT_SHA:-unknown}"
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
make VERSION="$VERSION" COMMIT="$COMMIT" DATE="$DATE" dist
```

**What this does:**

1. Install `make` (not in the Alpine Go image by default).
2. Set `VERSION` to the git tag (e.g. `v1.2.3`) or `dev` if not a tag.
3. Set `COMMIT` to the full git commit SHA.
4. Set `DATE` to the current UTC timestamp.
5. Run `make dist`, which cross-compiles for `linux/amd64` and `linux/arm64` and writes `dist/SHA256SUMS`.

The compiled binaries embed all three values via linker `-X` flags.  After a tag build you can verify:

```bash
./dist/backup-cleanup-linux-amd64 --version
# backup-cleanup v1.2.3 (commit abc1234..., built 2026-05-16T10:00:00Z)
```

**Why `${CI_COMMIT_TAG:-dev}`?**

This is a Woodpecker YAML substitution expression.  Woodpecker processes all `${VAR}` patterns in the YAML before the script runs, substituting known CI variables.  The `:-default` suffix tells Woodpecker to use `dev` if `CI_COMMIT_TAG` is empty (which it is on non-tag pushes).

### Step 5 — `release`

**Image:** `alpine:3.20`  
**When:** tag events only

This step publishes the compiled binaries to Gitea Releases.  It runs only when a tag is pushed.

**What it does, step by step:**

1. Install `curl` and `jq`.
2. Set shell variables: `GITEA_BASE`, `REPO`, `TAG` (from `$CI_COMMIT_TAG`).
3. Determine `PRERELEASE`: `true` if the tag contains a hyphen (e.g. `v1.0.0-rc.1`), `false` otherwise.
4. Check whether a Gitea release for this tag already exists (idempotency — safe to re-run).
5. If no release exists, create one via the Gitea REST API.
6. Upload four artifacts:
   - `dist/backup-cleanup-linux-amd64`
   - `dist/backup-cleanup-linux-arm64`
   - `dist/backup-cleanup-windows-amd64.exe`
   - `dist/SHA256SUMS`

**Important:** the `release` step reads the build artifacts produced by the `build` step.  Woodpecker pipeline steps in the same pipeline share the same workspace directory, so `dist/` is visible to both steps.

---

## 4. How Woodpecker variable substitution works

This is the most important concept to understand before editing `.woodpecker.yml`.

Woodpecker processes the YAML file **before** the container starts.  Every `${VARIABLE}` in the YAML is substituted at this stage:

- **Known Woodpecker CI variables** (like `${CI_COMMIT_TAG}`, `${CI_COMMIT_SHA}`) are substituted with their actual values.
- **Unknown variables** (like a shell variable you set earlier in the script) are substituted with an **empty string**.

This means:

```yaml
# WRONG — ${TAG} is not a Woodpecker variable.
# Woodpecker substitutes it to "" before the script runs.
commands:
  - |
    TAG="${CI_COMMIT_TAG}"
    echo "Tag is: ${TAG}"    # Always prints: "Tag is: "
```

```yaml
# CORRECT — $TAG (no braces) is passed through to the shell unchanged.
commands:
  - |
    TAG="$CI_COMMIT_TAG"
    echo "Tag is: $TAG"      # Prints the actual tag
```

**Rule of thumb:**

| Syntax | Processed by | Use for |
|---|---|---|
| `${CI_COMMIT_TAG}` | Woodpecker | Woodpecker CI variables only |
| `${CI_COMMIT_TAG:-dev}` | Woodpecker (with fallback) | Woodpecker CI variables with defaults |
| `$MY_SHELL_VAR` | Shell at runtime | All shell-local variables |
| `${MY_SHELL_VAR}` | **Woodpecker** → substitutes to `""` | **Never use for shell variables** |

---

## 5. Secrets

### `gitea_api_key`

| Property | Value |
|---|---|
| Name in Woodpecker | `gitea_api_key` |
| Used in step | `release` |
| Injected as | Environment variable `GITEA_API_KEY` |

**How to create the Gitea token:**

1. Open `https://git.digixoil.se/-/user/settings/applications`
2. Under "Manage Access Tokens", click "Generate Token"
3. Token name: `woodpecker-backup-cleanup` (or any descriptive name)
4. Permissions required:
   - `issue` — Gitea bundles the releases API under the issue category
   - `write:repository`
5. Copy the generated token immediately (it is shown only once)

**How to add the secret in Woodpecker:**

1. Go to `https://<woodpecker-url>/digixoil/backup-cleanup/settings`
2. Click "Secrets"
3. Click "Add Secret"
4. Name: `gitea_api_key`
5. Value: paste the token
6. Events: tick all (push, tag, pull\_request) — Woodpecker will only inject it where the `release` step runs anyway

**How the secret is used in the pipeline:**

```yaml
release:
  environment:
    GITEA_API_KEY:
      from_secret: gitea_api_key
  commands:
    - |
      curl -H "Authorization: token $GITEA_API_KEY" ...
```

The `environment` block injects the secret as an environment variable.  The shell script then references it as `$GITEA_API_KEY` (no braces, to avoid Woodpecker substitution — see §4).

---

## 6. Triggering the pipeline

### On every push to any branch

Steps `lint`, `vet`, `test`, and `build` run.

```bash
git push origin feature/my-change
```

### On a pull request

Steps `lint`, `vet`, `test`, and `build` run.

### On a semver tag push

All five steps run, including `release`.

```bash
git tag v1.2.3
git push origin v1.2.3
```

Or push all tags at once (not recommended for first-time setup):

```bash
git push --tags
```

See [doc/version-control.md](version-control.md) for the full branching and tagging workflow.

---

## 7. How a release is published

When a tag is pushed, the `release` step:

1. **Checks for an existing release** (`GET /api/v1/repos/digixoil/backup-cleanup/releases/tags/<TAG>`).  
   If the release already exists (e.g. from a previous re-run), the step reuses it and proceeds directly to uploading artifacts.

2. **Creates a new release** if none exists:
   ```json
   {
     "tag_name": "v1.2.3",
     "name": "v1.2.3",
     "body": "Release v1.2.3",
     "draft": false,
     "prerelease": false
   }
   ```

3. **Uploads four artifacts** as release assets:
   - `backup-cleanup-linux-amd64` — x86-64 binary
   - `backup-cleanup-linux-arm64` — ARM64 binary
   - `backup-cleanup-windows-amd64.exe` — Windows x86-64 binary
   - `SHA256SUMS` — checksum file for both binaries

After the pipeline finishes, the release is visible at:

```
https://git.digixoil.se/digixoil/backup-cleanup/releases
```

**Pre-release detection:** tags containing a hyphen (e.g. `v1.0.0-rc.1`, `v2.0.0-beta.3`) are automatically published as pre-releases (`"prerelease": true`).

---

## 8. Common failures and fixes

### `lstat ./...: no such file or directory`

**Cause:** `gofmt -l ./...` was used.  `./...` is not a valid argument to `gofmt`.

**Fix:** Use `gofmt -l .` (already corrected in the pipeline).

### Variables are empty in the `release` step

**Cause:** Shell variables referenced as `${MY_VAR}` were substituted to empty strings by Woodpecker's YAML engine before the script ran.

**Fix:** Use `$MY_VAR` (no curly braces) for all shell-local variables.  See §4 for details.

### `bad_habit: Consider adding a when block`

**Cause:** A step does not have an explicit `when:` event filter.

**Fix:** Add `when: - event: [push, tag, pull_request]` to the step (already corrected in the pipeline).

### `jq: invalid JSON text passed to --argjson`

**Cause:** `--argjson pre "${PRERELEASE}"` expanded to `--argjson pre ""` because `${PRERELEASE}` was substituted to empty by Woodpecker.

**Fix:** Use `$PRERELEASE` (no braces).  The value produced by the shell (`true` or `false`) is valid JSON for `--argjson`.

### Release step runs but `GITEA_API_KEY` is empty

**Cause 1:** The `gitea_api_key` secret has not been added to the Woodpecker repository settings.

**Cause 2:** The secret was added but the token has been revoked or expired in Gitea.

**Fix:** Regenerate the token at `https://git.digixoil.se/-/user/settings/applications` and update the secret in Woodpecker.

### Build step produces binaries but `--version` shows `dev`

**Cause:** The `make dist` command was run without `VERSION=`, `COMMIT=`, or `DATE=` arguments, so the Makefile defaults (`VERSION ?= dev`) applied.

**Fix:** Ensure the build step passes all three:
```sh
make VERSION="$VERSION" COMMIT="$COMMIT" DATE="$DATE" dist
```

---

## 9. Extending the pipeline

### Upgrading the Go version

Change the `&go_image` anchor at the top of `.woodpecker.yml`:

```yaml
variables:
  - &go_image 'golang:1.27-alpine'   # was 1.26
```

All steps that reference `*go_image` will automatically use the new version.

### Adding a code coverage step

```yaml
coverage:
  image: *go_image
  commands:
    - go test -count=1 -coverprofile=coverage.out ./...
    - go tool cover -func=coverage.out
  when:
    - event: [push, tag, pull_request]
```

### Adding a `linux/386` or `darwin/amd64` build target

1. Add a new target in the `Makefile`:
   ```makefile
   linux-386:
       mkdir -p $(DIST_DIR)
       CGO_ENABLED=0 GOOS=linux GOARCH=386 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
           -o $(DIST_DIR)/$(APP_NAME)-linux-386 $(CMD_PATH)
   ```
2. Add it to the `dist` target dependencies.
3. Add the artifact path to the `release` step's file loop in `.woodpecker.yml`.

### Running the pipeline locally with `woodpecker-cli`

```bash
# Install the CLI
go install go.woodpecker-ci.org/woodpecker/v2/cmd/woodpecker-cli@latest

# Simulate a pipeline run (lint step only)
woodpecker-cli exec --step lint .woodpecker.yml
```

This is useful for quickly checking YAML syntax and script logic without pushing to the server.

---

## 10. GitHub Actions equivalent

For GitHub-hosted mirrors, the repository includes:

```text
.github/workflows/ci.yml
```

The workflow mirrors the Woodpecker stages:

- `lint`: `gofmt -l .` format check
- `vet`: `go vet ./...`
- `test`: `go test -count=1 ./...`
- `build`: `make dist` with `VERSION`, `COMMIT`, and `DATE` linker metadata
- `release` (tags only): creates a GitHub release and uploads `dist/*` artifacts

Tag events (for example `v1.2.3`) trigger the release job; branch pushes and pull requests run validation plus build jobs without publishing a release.
