# GitHub Release and Linux Deployment Implementation Plan

**Goal:** Publish Sub2API quota center to `MengStar-L/sub2api5hlimit`, default it to `0.0.0.0:2556`, install it under `/opt/sub2api5hlimit`, and create Linux amd64/arm64 GitHub Releases from version tags.

**Architecture:** Keep the existing application and systemd service identity while changing the Go module, network fallback, and deployment paths. A tag-driven GitHub Actions workflow builds the embedded Vue SPA, verifies Go and Vue code, cross-compiles static binaries, assembles self-contained release archives, and publishes them with checksums through the GitHub CLI.

**Tech Stack:** Go 1.26, Vue 3/Vite, Bash, systemd, Nginx, GitHub Actions, GitHub CLI.

---

### Task 1: Align repository identity and application default

**Files:**
- Modify: `go.mod`
- Modify: `internal/httpapi/*.go`
- Modify: `internal/httpapi/*_test.go`
- Modify: `internal/syncer/*.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `web/vite.config.ts`

- **Step 1: Change the expected default in the config test**

  Update the config default test to require `Listen == "0.0.0.0:2556"`, the existing database path, and `CookieSecure == false` for direct public HTTP access. Retain a separate test proving an explicit `true` override works behind HTTPS.

- **Step 2: Run the focused test and verify it fails**

  Run: `go test ./internal/config -run TestLoadDefaults -count=1`

  Expected: FAIL because the current fallback is `127.0.0.1:2560`.

- **Step 3: Implement the new fallback and repository module path**

  Change `config.Load` to use `0.0.0.0:2556`, and point the Vite development proxy at `127.0.0.1:2556`. Change the module declaration and every internal import from `github.com/MengStar-L/sub2api-limit-portal/...` to `github.com/MengStar-L/sub2api5hlimit/...`.

- **Step 4: Verify focused and full Go tests**

  Run: `go test ./internal/config -run TestLoadDefaults -count=1`

  Expected: PASS.

  Run: `go test ./...`

  Expected: PASS with no old module import remaining.

- **Step 5: Commit**

  ```bash
  git add go.mod internal
  git commit -m "feat: update public listener and repository module"
  ```

### Task 2: Move packaged runtime state under `/opt/sub2api5hlimit`

**Files:**
- Modify: `packaging/sub2api-limit-portal.env.example`
- Modify: `packaging/sub2api-limit-portal.service`
- Modify: `packaging/nginx-sub2api-limit-portal.conf`
- Modify: `scripts/install.sh`
- Modify: `scripts/uninstall.sh`

- **Step 1: Define the exact path constants**

  Make the installer and uninstaller derive these paths from `INSTALL_ROOT=/opt/sub2api5hlimit`:

  ```text
  /opt/sub2api5hlimit/bin/sub2api-limit-portal
  /opt/sub2api5hlimit/config/sub2api-limit-portal.env
  /opt/sub2api5hlimit/data/app.db
  /opt/sub2api5hlimit/backups
  /opt/sub2api5hlimit/uninstall.sh
  ```

  Keep the unit at `/etc/systemd/system/sub2api-limit-portal.service`.

- **Step 2: Add fail-closed legacy layout detection**

  Before treating an installation as new, abort if the new install root is absent and any legacy binary, environment file, or database exists under `/usr/local`, `/etc/sub2api-limit-portal`, or `/var/lib/sub2api-limit-portal`. The error must direct the operator to the README migration section and must run before creating a master key or database.

- **Step 3: Update first-install, upgrade, backup, and rollback paths**

  Preserve the existing atomic replacement and stopped SQLite/WAL/SHM snapshot behavior. Create root-owned `bin`, `config`, and `backups` directories and a service-owned `data` directory. Generate an environment file containing:

  ```dotenv
  SUB2API_LIMIT_LISTEN=0.0.0.0:2556
  SUB2API_LIMIT_DB_PATH=/opt/sub2api5hlimit/data/app.db
  SUB2API_LIMIT_MASTER_KEY=<generated value>
  SUB2API_LIMIT_COOKIE_SECURE=false
  ```

- **Step 4: Update the hardened systemd unit and Nginx upstream**

  Set `WorkingDirectory`, `EnvironmentFile`, and `ExecStart` to `/opt/sub2api5hlimit`. Remove `StateDirectory`; retain the runtime directory and add `ReadWritePaths=/opt/sub2api5hlimit/data`. Keep Nginx as an optional example and change its `proxy_pass` to `http://127.0.0.1:2556`.

- **Step 5: Update uninstall behavior**

  A normal uninstall removes the binary, uninstaller, and unit but preserves `config`, `data`, and `backups`. `--purge` validates the exact `/opt/sub2api5hlimit` target, refuses symlinks or mount points, then removes the whole root and service account after explicit confirmation.

- **Step 6: Run static packaging checks**

  Run ShellCheck when available, parse both scripts with `bash -n` or a Bash parser, parse the PowerShell build script AST, and assert that active defaults use `0.0.0.0:2556` and `/opt/sub2api5hlimit`.

  Expected: all available checks PASS; references to legacy paths appear only in fail-closed migration detection and documentation.

- **Step 7: Commit**

  ```bash
  git add packaging scripts
  git commit -m "feat: install Linux service under opt"
  ```

### Task 3: Add tag-driven GitHub Release automation

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `internal/webui/dist/.gitkeep`
- Modify: `.gitignore`

- **Step 1: Keep generated files out of source history**

  Ignore `web/output/`, `dist/`, databases, Node modules, and built SPA files. Track `internal/webui/dist/.gitkeep` so Go's embed pattern remains valid in a clean checkout before the SPA build.

- **Step 2: Define the release trigger and least privilege**

  Trigger only on pushed tags matching `v*`, set `permissions: contents: write`, validate that the actual tag is a semantic `vMAJOR.MINOR.PATCH` version, and prevent simultaneous publication of the same tag with a workflow concurrency group.

- **Step 3: Implement verification and builds**

  Use official checkout, setup-go, and setup-node actions. Run:

  ```bash
  npm --prefix web ci
  npm --prefix web run typecheck
  npm --prefix web run test -- --run
  npm --prefix web run build
  go test ./...
  go vet ./...
  ```

  Build `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0`, `-trimpath`, and `-X main.version=${TAG#v}`.

- **Step 4: Assemble self-contained release archives**

  Create one archive per architecture containing `dist/<architecture binary>`, `packaging/`, `scripts/`, `README.md`, and `SECURITY.md` in the layout expected by `scripts/install.sh`. Generate a single SHA-256 file over both archives.

- **Step 5: Publish without a third-party release action**

  Run `gh release create "$GITHUB_REF_NAME" release/*.tar.gz release/SHA256SUMS --verify-tag --generate-notes` with the repository `GITHUB_TOKEN`.

- **Step 6: Validate the workflow locally**

  Parse YAML, search for required trigger/permissions/build commands, and reproduce the cross-compiles and archive/checksum layout locally.

- **Step 7: Commit**

  ```bash
  git add .github .gitignore internal/webui/dist/.gitkeep
  git commit -m "ci: publish Linux binaries from version tags"
  ```

### Task 4: Rewrite Linux deployment and systemd documentation

**Files:**
- Modify: `README.md`
- Modify: `SECURITY.md`

- **Step 1: Lead with the operational contract**

  Explain that Sub2API enforces the quotas, the portal displays state, the service binds `0.0.0.0:2556`, and users can open `http://server-ip:2556` directly. State that Nginx HTTPS and Secure Cookie are optional administrator-managed hardening.

- **Step 2: Add Release download and checksum commands**

  Provide copyable commands for detecting amd64/arm64, downloading the `v0.1.0` archive and `SHA256SUMS`, validating it with `sha256sum`, extracting it, and invoking `sudo bash scripts/install.sh`.

- **Step 3: Document Nginx, DNS, certificates, and firewall**

  Explain replacing `quota.example.com`, configuring a valid certificate, testing and reloading Nginx when the administrator chooses HTTPS. Do not make Nginx or firewall rules prerequisites for direct TCP 2556 access.

- **Step 4: Document first-run and systemd operations**

  Include Setup Token retrieval and these commands:

  ```bash
  systemctl status sub2api-limit-portal
  systemctl start sub2api-limit-portal
  systemctl stop sub2api-limit-portal
  systemctl restart sub2api-limit-portal
  systemctl enable sub2api-limit-portal
  journalctl -u sub2api-limit-portal -f
  ```

- **Step 5: Document backup, upgrade, uninstall, and legacy migration**

  Use the `/opt/sub2api5hlimit` paths throughout. Require preserving the original environment master key with SQLite backups. Provide an explicit stopped-service migration path from the legacy layout rather than allowing the installer to guess.

- **Step 6: Verify documentation consistency**

  Search for obsolete default port/path/repository references, validate local Markdown links, and ensure all deployment commands match the scripts and unit.

- **Step 7: Commit**

  ```bash
  git add README.md SECURITY.md .openteams/plans
  git commit -m "docs: add Linux release and systemd guide"
  ```

### Task 5: Final verification, push, tag, and observe the release

**Files:**
- Verify all tracked source and release files.

- **Step 1: Run the complete local gate**

  Run:

  ```bash
  go test -count=1 ./...
  go vet ./...
  npm --prefix web run typecheck
  npm --prefix web run test -- --run
  npm --prefix web run build
  npm --prefix web run test:e2e
  npm --prefix web run test:e2e:production
  pwsh -NoProfile -File scripts/build-linux.ps1 -Version 0.1.0 -SkipWeb
  ```

  Expected: all checks PASS except `go test -race` remains unavailable on this Windows host if no C compiler is installed; report that limitation explicitly.

- **Step 2: Audit tracked content and secrets**

  Ensure preview databases, full API keys, master keys, logs, browser reports, `node_modules`, and built local binaries are not staged. Confirm the only tag is the intended release tag.

- **Step 3: Configure and push the empty target repository**

  Add `origin=https://github.com/MengStar-L/sub2api5hlimit.git`, verify it still has no branches or tags, then push `main`.

- **Step 4: Create and push the annotated release tag**

  ```bash
  git tag -a v0.1.0 -m "Release v0.1.0"
  git push origin v0.1.0
  ```

- **Step 5: Monitor GitHub Actions to completion**

  Use `gh run list` and `gh run watch --exit-status`. If the workflow fails, inspect its logs, apply the smallest correction, commit and move the unconsumed tag only if no Release was created, then re-run verification and push again.

- **Step 6: Verify the public release**

  Confirm the GitHub Release is public, both architecture archives and `SHA256SUMS` are attached, the checksums match downloaded assets, and the tagged source contains the deployment README.
