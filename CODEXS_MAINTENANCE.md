# Codexs Maintenance Boundary

This fork tracks `router-for-me/CLIProxyAPI` while keeping Codexs-specific behavior isolated.

## Branches and remotes

- `origin` is the Codexs fork.
- `upstream` is the original CLIProxyAPI repository and must never be pushed to.
- `codexs/main` is the deployable baseline.
- `feature/*` branches contain one focused Codexs change.
- `upgrade/*` branches are reserved for upstream release merges.

Do not deploy directly from a local working tree. Build a tagged image from a reviewed commit and pin the resulting digest in the deployment repository.

## Customization rule

Keep Codexs code in `internal/codexs/` whenever possible. Core changes must be limited to explicit integration points and must have regression coverage. Do not modify model catalog files merely to advertise a model; public models require a successful upstream verification request.

## Required checks

Every change must pass:

```bash
go test ./...
go build -o test-output ./cmd/server
rm test-output
```

Run `gofmt -w` only on Go files changed by the Codexs branch. Upstream releases
can contain pre-existing formatting differences, so the fork does not rewrite
unrelated upstream files merely to make an upgrade pass.

The candidate image must also pass `scripts/codexs-canary.sh` on the VPS before production deployment.

## Secrets

Never commit API keys, OAuth tokens, auth JSON files, deployment configuration, or production hostnames. Canary credentials are mounted read-only and canary state is always stored in an isolated writable directory.
