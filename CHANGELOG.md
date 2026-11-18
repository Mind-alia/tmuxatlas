# Changelog

## Unreleased

### Features

* refresh the Atlas interface with an attention inbox, session previews, quick replies, window tabs, Wall and Monitoring views
* add project directory and agent selection for new sessions, host aliases and navigation shortcuts
* add a sudo terminal: agents propose a command with `tmuxatlas sudo-request`, an administrator approves the exact version and gets an interactive shell on a pinned `--sudo-terminal` peer with the command typed at the prompt, not run; the agent only learns done or refused and a note (see `docs/sudo-terminal.md`)

### Security

* reject sudo commands and reasons with control, format or separator characters, runs of blanks or more than 1000 characters; show them whole with length and sha256
* ignore OSC 52 clipboard reads and writes from panes
* install guest binaries only from the root-owned `/opt/tmuxatlas`; add `docs/deploiement.md`
* warn permanently on hosts that are neither the hub nor a pinned sudo peer
* keep a silent peer from stalling the API (no lock held during handlers, cached and rate-limited owner checks)
* limit logins per username; trust `X-Forwarded-For` only with the Caddy secret (`TMUXATLAS_PROXY_SECRET`)
* accept sudo requests only from `TMUXATLAS_SUDO_REQUESTERS`, with per-host caps
* bind push subscriptions to accounts and filter recipients at send time
* bound file listings and reads, close deny-list gaps, refuse `TMUXATLAS_FILES=rw` on the hub
* close the hub's own microphone on revocation; compare attachment tokens in constant time

## [0.9.0](https://github.com/LosFurina/tmuxatlas/compare/v0.8.0...v0.9.0) (2026-07-27)

### Features

* add a unified Command Palette for hosts, sessions, windows, agents, and application actions
* add Terminal search, contextual clipboard actions, paste confirmation, and true Zen Mode
* add a generation-safe mobile Input Composer with exact-text submission
* improve responsive navigation, loading and error states, accessibility, and visual consistency
* add browser, interaction, accessibility, and visual-regression coverage for the terminal workspace

## [0.8.0](https://github.com/LosFurina/tmuxatlas/compare/v0.7.0...v0.8.0) (2026-07-27)

### Features

* add revisioned canonical state sync and resilient browser reconnect lifecycle
* persist Push subscriptions and expose multi-host Fleet Health
* add signed transactional self-update with readiness checks and rollback
* add mobile terminal controls, responsive drawer, and safe-area support
* self-host application and terminal fonts with reproducible Nerd Symbols subset

## [0.7.0](https://github.com/LosFurina/tmuxatlas/compare/v0.6.0...v0.7.0) (2026-07-26)

### Breaking changes

- Added mandatory Peer runtime protocol v1 negotiation. Upgrade the Hub first,
  then every Agent; legacy Agents remain offline until upgraded.
- Session mutations and remote terminal opens require an explicit immutable
  `host_id` plus session target. Missing-host fallback to Hub-local tmux was
  removed.
- Remote PTYs now use generation-bound framed data/control messages, validated
  resize, and one-time attachment tokens.

### Reliability and operations

- Added correlated action results, bounded Agent outcome deduplication,
  generation-safe Peer replacement, and `execution-unknown` handling across
  Agent process changes.
- `tmuxatlas peers remove` now performs live atomic revocation through the
  private Hub Unix socket and immediately tears down requests and PTYs.

## [0.1.3-beta.2](https://github.com/LosFurina/tmuxatlas/compare/v0.1.2-beta.2...v0.1.3-beta.2) (2026-04-20)


### Bug Fixes

* build actor allowance ([2ab0a2d](https://github.com/LosFurina/tmuxatlas/commit/2ab0a2de0bdb408456be2f8826781352e64b9750))

## [0.1.2-beta.2](https://github.com/LosFurina/tmuxatlas/compare/v0.1.1-beta.2...v0.1.2-beta.2) (2026-04-12)


### Bug Fixes

* shortcut overlap ([f4af952](https://github.com/LosFurina/tmuxatlas/commit/f4af9522a27c6e6b10bb4699c4178e3b78164534))
* shortcut overlap ([35b1b11](https://github.com/LosFurina/tmuxatlas/commit/35b1b1174b608166ce845a71e35fbcb957c27fce))

## [0.1.1-beta.2](https://github.com/LosFurina/tmuxatlas/compare/v0.1.0-beta.2...v0.1.1-beta.2) (2026-03-15)


### Features

* better font/size ([a607c16](https://github.com/LosFurina/tmuxatlas/commit/a607c162761eac26e2dec4eaebf637d07b0cca61))
* better font/size ([a5cf00b](https://github.com/LosFurina/tmuxatlas/commit/a5cf00bc68d50fd4d78fb121d8c2520210df6f77))
