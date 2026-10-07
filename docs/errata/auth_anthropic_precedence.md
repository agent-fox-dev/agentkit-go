# Erratum: `ANTHROPIC_API_KEY` resolves before the bearer variables

**Relates to:** REQ-AUTH-03, ruling L-12, issue #85.
**Status:** implemented 2026-10-07.

## What the PRD says

REQ-AUTH-03: "Anthropic resolves `ANTHROPIC_AUTH_TOKEN` (sent as
`Authorization: Bearer`, **not** `x-api-key`) > `ANTHROPIC_OAUTH_TOKEN` >
`ANTHROPIC_API_KEY`."

## Why the code diverges

The official Anthropic SDKs resolve the API key first. A machine that carries
both an API key and a bearer token therefore authenticated differently under
AgentKit than under every first-party client on the same machine, with a
different account, rate limit and bill, and nothing in a 401 said which
variable was picked.

## What changed

- `anthropic.VendorAuth` is ordered `ANTHROPIC_API_KEY` > `ANTHROPIC_AUTH_TOKEN`
  > `ANTHROPIC_OAUTH_TOKEN`. The per-variable schemes are unchanged.
- The Vertex deployment resolves the environment through its own table, which
  reads `ANTHROPIC_AUTH_TOKEN` only. That variable is how a Google access token
  is supplied (`gcloud auth print-access-token`); the API key and the OAuth
  token are Anthropic-issued and are never sent to a Google endpoint (L-12).
  Without this, a leftover API key would now outrank the Google token, and then
  be dropped by the Vertex narrowing, leaving no credential at all.
- A bearer from `ANTHROPIC_OAUTH_TOKEN`, or any `sk-ant-oat` bearer, carries
  `anthropic-beta: oauth-2025-04-20` on the direct deployment.

## Who notices

Only a process with `ANTHROPIC_API_KEY` set alongside `ANTHROPIC_AUTH_TOKEN`
or `ANTHROPIC_OAUTH_TOKEN`, on the direct deployment: it now sends the API key.
Unset the API key to keep using the bearer.
