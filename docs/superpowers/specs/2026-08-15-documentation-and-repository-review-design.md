# Documentation And Repository Review Design

## Goal

Bring the user-facing documentation into line with the current full Docker Compose stack, then perform a submission-readiness review of the repository without committing or pushing changes.

## Documentation Scope

Update `README.md`, `QUICKREF.md`, and `MONITORING-QUICKREF.md`. Docker Compose becomes the primary startup path and must consistently use `docker compose -p smart-router`. Local Go/Vite execution remains documented as an optional development workflow.

The documentation must explain that Compose starts Gateway, Checker, PostgreSQL, and Redis; Checker publishes no host port; inference probes can incur upstream charges and are limited by the configured budgets; admin APIs require a Bearer admin key; named data volumes must be preserved; and migrations in `/docker-entrypoint-initdb.d` only run automatically for a new PostgreSQL volume.

Remove references to directories or log files that do not exist in the repository. Replace them with commands verified against the current files and running Compose project.

## Review Scope

Review tracked project files by responsibility:

- Docker image and Compose service definitions
- Go entrypoints, configuration, routing, Checker, API, storage, replay, and tests
- Vue/Vite source, package metadata, and production build
- PostgreSQL migrations and their ordering
- authentication, monitoring, startup, and verification shell scripts
- Prometheus rules/configuration and Grafana JSON
- Git status, branch state, ignored files, and accidental secret indicators

The review prioritizes correctness, deployment hazards, security-sensitive defaults, stale documentation, and missing validation. Findings that can be fixed safely within the documentation scope are corrected. Code or operational defects are reported with file and line references unless a narrow correction is necessary to make repository verification pass.

## Verification

Acceptance requires fresh evidence from Markdown consistency checks, `docker compose config`, Docker image build, full Go tests, frontend production build, shell syntax checks where the required shell is available, JSON parsing, Prometheus configuration/rule validation when a suitable container is available, and live Gateway/Checker/PostgreSQL/Redis checks.

No Git commit, push, branch deletion, volume deletion, or destructive cleanup is performed.
