# Codexs Deployment History

## 2026-08-17 — CLIProxy upstream v7.2.135

- Source sync commit: `3f4c2db9` (`upstream/main` tag `v7.2.135`).
- Canary runbook authentication fix: `055d866d`.
- Production image: `codexs/cli-proxy-api:upstream-v7.2.135-3f4c2db9`.
- Verified public routes: `/v1/models`, Gemini `gemini-3.7-flash-high`, Codex `gpt-5.6-sol`, and Claude `claude-opus-4-6-thinking`.
- Immediate rollback image: `codexs/cli-proxy-api:mixed-tools-fix-20260817`.
- Rollback source change: `c5677308` (`fix(antigravity): handle mixed built-in tools`).

## Retention Policy

- Keep source history in Git; do not store Docker image archives in the repository.
- On the VPS, retain the active image and one validated rollback image.
- Keep deployment configuration backups separately from the repository and never commit credentials.
