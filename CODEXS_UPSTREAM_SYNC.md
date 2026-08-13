# Upstream Sync Runbook

## Prepare an upgrade

1. Confirm `codexs/main` is green in GitHub Actions.
2. Ensure the local working tree is clean.
3. Fetch the desired upstream tag and create an isolated upgrade branch:

```bash
scripts/codexs-sync-upstream.sh vX.Y.Z
```

4. Resolve only conflicts caused by Codexs changes. Do not alter unrelated upstream code.
5. Run the required checks in `CODEXS_MAINTENANCE.md`.
6. Push `upgrade/vX.Y.Z` and open a pull request into `codexs/main`.

## Deploy an approved upgrade

1. Build an immutable image tag from the merged commit.
2. Run the VPS canary with a read-only auth mount and isolated state directory.
3. Verify existing production models, especially one Codex, one Gemini, and one Claude request.
4. Pin the image digest in the deployment repository.
5. Restart only `cli-proxy-api` during the deployment window.
6. Verify `/v1/models`, a real request, and the CPAMP usage event after deployment.

## Roll back

Restore the prior pinned image digest and restart only `cli-proxy-api`. Do not change public keys, Nginx routes, CPAMP, or TKProxy during a CLIProxy rollback.
