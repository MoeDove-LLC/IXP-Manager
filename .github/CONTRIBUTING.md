# Contributing to MoeDove IXP Manager

Use `go-rewrite` as the base for Go migration changes. The [README](../README.md) describes the Go and Node toolchains and how to run the application locally. The [migration checklist](../docs/MIGRATION.md) tracks the old workflows and their Go replacements.

Build with `make build` and run checks with `make test`. Include any focused regression commands or isolated fixture results in the pull request. Make a separate commit for each independently verifiable change and use `git diff --check` before committing.

Open pull requests against `go-rewrite` in [MoeDove-LLC/IXP-Manager](https://github.com/MoeDove-LLC/IXP-Manager). Describe what changed, how it was verified, and any impact on an existing installation or on migration from the legacy application.
