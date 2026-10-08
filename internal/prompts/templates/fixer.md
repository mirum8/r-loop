# Fix a stuck step

An r-loop run in `{{.Root}}` is held by a blocker that a retry will not clear. Find out why and propose a fix. The maintainer reads your proposal and decides; r-loop applies it, never you.

## The incident

Read `{{.IncidentPath}}` first. It holds the blocker and its excerpt, the watchdog's diagnosis, every provider's version at run start and now, the run's last records, the resolved config with where each value came from, and the paths: the primary root, the step's worktree and the run folder `{{.RunDir}}`.

## How to investigate

Use read-only commands only: provider versions, `--help` output, release notes and changelogs, the config files, the run's logs and records under `{{.RunDir}}`, and the screens of the panes named in the incident.

Change no file, in the repository, its worktrees or anywhere else on the machine, and install, upgrade, downgrade or log in to nothing. r-loop checks the primary tree and the step's worktree when you finish, and a changed tree fails the fix.

## The proposal

Write `{{.ProposalPath}}` as one JSON object:

```json
{"kind": "config", "cause": "", "evidence": "", "commands": [], "config": {"file": "", "content": ""}, "manual": [], "risk": ""}
```

- `kind` is one of:
  - `env` — the machine needs a change: a provider to downgrade or upgrade, a tool to install. Put the exact shell commands in `commands`, in the order to run them; they run in the fix folder, one at a time, and the first one that fails stops the rest.
  - `config` — an r-loop config file needs a change, such as a provider flag a new release renamed. Put the whole new file in `config`: `file` is `.r-loop/config.yaml` or `~/.config/r-loop/config.yaml`, and `content` is that file's complete new text, block-style YAML, never a diff or a fragment. It must load as an r-loop config, or the proposal is refused.
  - `r-loop` — a bug in r-loop itself. Name the file and the line in `cause` or `evidence`. Leave `commands` and `config` out.
  - `project` — a fault in the project being built, not in the agents or r-loop. Leave `commands` and `config` out.
- `cause` is one sentence naming what is wrong; `evidence` is what you saw that shows it: a version, a line of help output, a release note, a screen.
- `manual` lists what the maintainer must do by hand, such as a login or anything else interactive. Never put an interactive step in `commands`.
- `risk` says what the fix could break and how to undo it.

`commands` and `config` are only for `env` and `config`; leave either out when that fix needs none.
{{template "outcome" .}}