# WeClaw Architecture Boundary

`weclaw` is the WeChat bridge core. In downstream products it should stay focused on channel semantics, normalized agent input, and remote cowork launch, not product runtime orchestration.

## Product Role

- Ingest WeChat messages and media items.
- Normalize inbound content into a canonical agent-facing shape.
- Persist workspace facts that downstream runtimes can consume.
- Expose stable bridge-facing CLI and config interfaces.
- Act as a `remote cowork companion` for downstream products such as Longclaw.

## Responsibilities That Stay In WeClaw

- WeChat protocol and message parsing.
- Media ingestion for text, image, file, voice, and video.
- Transcript-first voice normalization based on `VoiceItem.Text`.
- Session window, media index, and sidecar persistence.
- Agent input canonicalization.
- Reviewed handoff compatibility contract and formal/debug reviewed-write boundary.
- Reviewed handoff audit fields and metadata schema.

## Responsibilities That Stay Out Of WeClaw

- Launchd or desktop process supervision.
- Runtime service orchestration and watchdog loops.
- TLS preflight and host-level health remediation.
- Desktop UI, settings panels, and task center UX.
- Evidence review, repair queues, and governance console state.
- Environment-specific policy defaults that can be injected via config.

## Stable Interfaces For Downstream Products

Downstream runtimes such as `longclaw-agent-os` should consume `weclaw` through stable boundaries:

- `~/.weclaw/config.json`
- `weclaw` CLI commands
- Workspace/session/sidecar files under `~/.weclaw`
- reviewed handoff compatibility contract

Current runtime-facing policy knobs include:

- `voice_input_mode_default`
- `archive_tool_enabled`
- `obsidian_enabled`
- `obsidian_formal_write_enabled`
- `agent_input_policy`

These values are configuration inputs. Their implementations remain inside `weclaw`.
Some names still use legacy `archive` / `formal write` terminology for compatibility, but downstream product language should describe them as reviewed handoff controls.

## Forking Guidance

`Gemini-Nick/weclaw` should remain close to `fastclaw-ai/weclaw` by only carrying bridge-core capabilities that are useful to any downstream consumer.

Examples of acceptable fork extensions:

- Better WeChat media handling
- Voice canonicalization improvements
- Stable reviewed handoff compatibility contracts
- Session/media persistence improvements

Examples that should live in downstream products instead:

- Desktop-only UX
- Runtime installers and launch agents
- Environment policy selection
- Product-specific default homes, studios, dashboards, and governance consoles
