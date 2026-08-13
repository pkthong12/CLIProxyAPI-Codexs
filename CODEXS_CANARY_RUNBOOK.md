# Codexs Canary Runbook

Run this script on the VPS from a clean checkout or release directory. It never touches the production container and removes its temporary container when it exits.

```bash
export CODEXS_IMAGE='registry.example/cli-proxy-api-codexs@sha256:...'
export CANARY_CONFIG_FILE='/absolute/path/config.local.yaml'
export CANARY_AUTH_DIRECTORY='/absolute/path/auth-files'
export CANARY_DATA_DIRECTORY='/absolute/path/canary-data'
export CANARY_API_KEY_FILE='/absolute/path/client-api-key.txt'
export EXPECTED_MODEL='gemini-3.6-flash-high'

scripts/codexs-canary.sh
```

The auth directory is read-only. The script validates readiness and `/v1/models`; it does not send a paid generation request. Run explicit provider regression requests only after the catalog check passes and keep those requests outside the public route.

If the canary fails, retain the production image unchanged, save only redacted logs, and investigate from the candidate commit.
