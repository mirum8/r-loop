# Find the gate

Work in `{{.Worktree}}` on `{{.Base}}`. Find the one shell command that builds this project and runs its whole test suite, the way its maintainers run it before a merge: read the build files, the README, CLAUDE.md, CI configuration and any Makefile or task runner first.

Run the command from the repository root and check that it passes. If it fails for a reason outside the code, such as a missing tool or service, choose a command that the repository can pass on its own.

Write the command in backticks, on its own line, to `{{.ReportPath}}`. Under it, write one sentence that names where you found it. Change no other file, and remove any file the build leaves that is not ignored by git.
{{template "sentinel" .}}
