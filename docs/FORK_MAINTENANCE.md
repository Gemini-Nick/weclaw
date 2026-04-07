# Fork Maintenance

This fork should stay close to `fastclaw-ai/weclaw` while preserving bridge-core capabilities that are valuable to downstream products.

## Fork-Core vs Downstream

Keep in this repo:

- WeChat message semantics
- media ingestion and persistence
- transcript-first voice behavior
- session/media facts
- archive contracts and formal/debug boundaries

Do not grow this repo with:

- launchd installers
- guardian/watchdog loops
- desktop UI
- product-specific runtime orchestration

Those belong in downstream products such as `longclaw-agent-os`.

## Upstream Sync Workflow

1. Keep `main` clean and linear.
2. Land local bridge-core changes in small thematic commits.
3. Create an integration branch from `main`.
4. Merge or rebase `upstream/main` into that branch.
5. Run `scripts/verify-fork-core.sh`.
6. Run real WeChat behavior checks for text, image, file, voice, and archive flows.
7. Only merge back to `main` after both code and behavior checks pass.

## Verification Standard

Code gate:

- `go build ./...`
- `scripts/verify-fork-core.sh`

Behavior gate:

- text messaging
- image save and dispatch
- file save and dispatch
- voice transcript-first behavior
- Obsidian formal/debug behavior if enabled in this fork

## Downstream Consumption

Downstream runtimes should consume a verified `weclaw-real` built from a known commit or tag in this fork.

They should not:

- track `upstream/main` directly
- consume local uncommitted binaries
- reimplement WeChat semantics outside this repository
