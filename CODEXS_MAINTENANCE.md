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

## Client API key safety

Client API keys are enforced by CLIProxy's api-keys list in the deployed
config.local.yaml. CPAMP stores management, analytics, and API-key alias data,
but it does not independently authorize requests to /v1/*.

Before a CLIProxy image or deployment configuration change:

1. Create a timestamped backup of the deployed config.local.yaml.
2. Record only the count and SHA-256 fingerprints of configured client keys;
   never place plaintext keys in logs, tickets, commits, or shell history.
3. After the container reloads, test each required client key against
   GET /v1/models and verify its required model IDs are present.
4. Run one minimal POST /v1/chat/completions request with a non-production
   prompt for any key whose provider route must be confirmed.

If a previously valid key returns 401 Invalid API key:

1. Check whether its SHA-256 fingerprint is present in the deployed api-keys
   list. This failure occurs before provider routing.
2. Do not change providers, model routing, Nginx, CPAMP, or unrelated keys.
3. Restore only the missing key from the approved secret source, retaining a
   timestamped configuration backup first.
4. Wait for CLIProxy's configuration watcher to reload, then repeat the two
   API checks above and confirm all production containers remain running.

Treat any key copied into chat, screenshots, terminal history, or logs as
exposed: create a replacement key and revoke the exposed key after recovery.
