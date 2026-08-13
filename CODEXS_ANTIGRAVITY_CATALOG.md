# Codexs Antigravity Model Discovery

## Purpose

The Codexs fork can discover the model metadata returned by Antigravity's
`fetchAvailableModels` endpoint. Discovery is deliberately separate from the
public model registry: a discovered model is never listed by `/v1/models` and
cannot receive customer traffic until a later verifier explicitly marks it as
`verified`.

This prevents a UI label, a temporary experiment, or an unconfirmed raw model
ID from being exposed as a supported public API model.

## Enable discovery

Keep the setting disabled in production until the candidate image has passed
the VPS canary. To enable discovery in a non-production candidate config:

```yaml
antigravity:
  model-catalog:
    enabled: true
    snapshot-path: "state/antigravity-model-catalog.json"
    refresh-interval: "6h"
```

`refresh-interval` must be a positive Go duration. If `snapshot-path` is
relative, it is resolved against the directory containing `config.yaml`.

`verify-enabled` is a separate opt-in. When enabled, one minimal native
`generateContent` request is made for each pending candidate up to
`max-verifications-per-run` (default: `1`). Only a successful response marks a
candidate as `verified`. A 404 marks it `rejected`; other failures such as rate
limits leave it pending. `expose-verified` is a second opt-in and only exposes
models verified by the exact same credential fingerprint.

The worker selects the first enabled Antigravity auth file that already has an
access token. It does not refresh tokens, modify auth files, or copy any
credential data into the snapshot. A missing or expired usable credential leaves
the last successful snapshot unchanged and writes a generic failure status.

## Snapshot lifecycle

The snapshot is local-only and written with restrictive file permissions. It
contains only model metadata and these states:

- `pending_verification`: newly discovered or reappeared model; never public.
- `verified`: passed one controlled native request and is eligible for public
  registration only when `expose-verified` is enabled.
- `rejected`: received a 404 from a controlled verification request; never public.
- `stale`: model was absent from the latest successful discovery response;
  never public unless it is rediscovered and verified again.

## Promotion rules

Before adding any discovered model to the public registry:

1. Run the candidate image using `scripts/codexs-canary.sh` with read-only
   production auth mounts and isolated state.
2. Perform one rate-limited minimal upstream generation request for the exact
   raw model ID.
3. Persist only its state and sanitized error category; do not persist prompts,
   responses, headers, tokens, or auth metadata.
4. Promote only `verified` models and ensure a remote `models.json` refresh
   cannot erase them.
5. Deploy a pinned image digest only after the candidate canary passes.

No static alias such as `gemini-3.7-flash` should be published until its raw
upstream ID has passed those checks.

## Rollback

Set `antigravity.model-catalog.enabled: false` and restart only the candidate
or production service during an approved deployment window. The worker stops
on process shutdown. The existing snapshot can remain on disk because it is
not read by public routing or `/v1/models` in this phase.
