# WeClaw Architecture Boundary

`weclaw` is the WeChat bridge core. It should stay focused on channel semantics and normalized agent input, not product runtime orchestration.

## Product Role

- Ingest WeChat messages and media items.
- Normalize inbound content into a canonical agent-facing shape.
- Persist workspace facts that downstream runtimes can consume.
- Expose stable bridge-facing CLI and config interfaces.

## Responsibilities That Stay In WeClaw

- WeChat protocol and message parsing.
- Media ingestion for text, image, file, voice, and video.
- Transcript-first voice normalization based on `VoiceItem.Text`.
- Session window, media index, and sidecar persistence.
- Agent input canonicalization.
- Obsidian archive tool contract and formal/debug archive boundary.
- Formal note audit fields and archive metadata schema.

## Responsibilities That Stay Out Of WeClaw

- Launchd or desktop process supervision.
- Runtime service orchestration and watchdog loops.
- TLS preflight and host-level health remediation.
- Desktop UI, settings panels, and task center UX.
- Environment-specific policy defaults that can be injected via config.

## Stable Interfaces For Downstream Products

Downstream runtimes such as `longclaw-agent-os` should consume `weclaw` through stable boundaries:

- `~/.weclaw/config.json`
- `weclaw` CLI commands
- Workspace/session/sidecar files under `~/.weclaw`
- Obsidian archive tool contract

Current runtime-facing policy knobs include:

- `voice_input_mode_default`
- `archive_tool_enabled`
- `obsidian_enabled`
- `obsidian_formal_write_enabled`
- `agent_input_policy`

These values are configuration inputs. Their implementations remain inside `weclaw`.

## Forking Guidance

`Gemini-Nick/weclaw` should remain close to `fastclaw-ai/weclaw` by only carrying bridge-core capabilities that are useful to any downstream consumer.

Examples of acceptable fork extensions:

- Better WeChat media handling
- Voice canonicalization improvements
- Stable archive contracts
- Session/media persistence improvements

Examples that should live in downstream products instead:

- Desktop-only UX
- Runtime installers and launch agents
- Environment policy selection
- Product-specific dashboards and control planes
