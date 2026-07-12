# Contributing to Thanos

Thanks for your interest in contributing! This guide covers the basics.

## Quick Start

1. **Fork** the repository on GitHub.
2. **Clone** your fork locally:
   ```bash
   git clone https://github.com/<your-username>/Thanos.git
   cd Thanos
   ```
3. **Build** to verify your environment:

   ```bash
   # Windows
   .\scripts\build.ps1

   # Linux
   CGO_ENABLED=1 go build -o thanos ./cmd/thanos
   ```

4. **Create a branch** for your work (see [Branching](#branching) below).

## Branching

Use a short, descriptive branch name prefixed by the type of change:

| Prefix      | Use for                                 | Example                 |
| ----------- | --------------------------------------- | ----------------------- |
| `feat/`     | New features                            | `feat/discord-commands` |
| `fix/`      | Bug fixes                               | `fix/sniffer-crash`     |
| `docs/`     | Documentation only                      | `docs/readme-update`    |
| `refactor/` | Code restructuring (no behavior change) | `refactor/orchestrator` |
| `chore/`    | Build scripts, deps, CI, tooling        | `chore/update-deps`     |

```bash
git checkout -b feat/your-feature
```

## Making Changes

- **Keep PRs focused.** One feature or fix per pull request. If you need to change multiple things, open multiple PRs.
- **Follow existing code style.** This is a Go project — run `gofmt` and `go vet` before committing:
  ```bash
  gofmt -w .
  go vet ./...
  ```
- **Don't commit generated files.** The `thanos.db*` database, `/docker/` compose files, and `/bin/` build artifacts are gitignored. Keep them out of your PR.
- **No secrets.** Never commit credentials, tokens, API keys, or personal config. All runtime config lives in `thanos.db` (SQLite), which is gitignored.

## Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/) format:

```
<type>: <short description>

<optional body explaining why, not what>
```

Types: `feat`, `fix`, `docs`, `refactor`, `chore`, `test`

Examples:

```
feat: add per-server traffic history endpoint
fix: prevent sniffer panic on nil interface
docs: document versioning in README
```

## Pull Requests

1. **Push** your branch to your fork:
   ```bash
   git push origin feat/your-feature
   ```
2. **Open a PR** against the `main` branch of the upstream repo.
3. **Fill in the PR template** (if present) — describe what changed and why.
4. **Link related issues** in the PR description (e.g. `Closes #42`).
5. **Keep PRs small** — under ~500 lines of diff when possible. Large changes are harder to review and slower to merge.

### PR Checklist

- [ ] Branch name follows the prefix convention
- [ ] Commit messages follow Conventional Commits
- [ ] `gofmt` and `go vet` pass
- [ ] No secrets, database files, or build artifacts committed
- [ ] Existing functionality still works (manual test if no automated tests exist)

## Reporting Bugs

Open a [GitHub Issue](../../issues) with:

1. **Thanos version** — check the web UI footer or the startup log
2. **OS** — Windows or Linux, and version
3. **Steps to reproduce** — what you did, what happened, what you expected
4. **Logs** — paste relevant log output (redact any personal info)

## Requesting Features

Open a [GitHub Issue](../../issues) with the `enhancement` label. Describe the use case, not just the solution — we may have a simpler way to achieve the same goal.

## Code of Conduct

Be respectful and constructive. Harassment, personal attacks, and trolling won't be tolerated.
