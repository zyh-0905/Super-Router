# Documentation And Repository Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Synchronize the project documentation with the full Compose deployment and produce a verified submission-readiness review without committing changes.

**Architecture:** Treat `README.md` as the authoritative guide, keep `QUICKREF.md` as a concise operational card, and limit `MONITORING-QUICKREF.md` to the monitoring add-on. Validate documentation statements against source, rendered Compose configuration, builds, tests, and the live stack.

**Tech Stack:** Markdown, Go 1.26, Vue 3, Vite 6, Docker Compose v2, PostgreSQL 16, Redis 7, Prometheus

---

### Task 1: Establish Documentation Drift

**Files:**
- Test: `README.md`
- Test: `QUICKREF.md`
- Test: `MONITORING-QUICKREF.md`

- [x] Run `rg -n "docker-compose|docs-archive|web-legacy|/tmp/sr-gateway|start-monitoring\.sh" README.md QUICKREF.md MONITORING-QUICKREF.md` and record the stale commands and paths.
- [x] Render `docker compose -p smart-router config --format json` and use the output as the source of truth for services, ports, mounts, dependencies, and restart policies.

### Task 2: Update The Primary Guide

**Files:**
- Modify: `README.md`

- [x] Replace the quick-start section with `docker compose -p smart-router up -d --build`, status/log commands, the Web URL, and a four-service explanation.
- [x] Add prerequisites, admin authentication, Checker cost/budget behavior, named-volume safety, and existing-database migration notes.
- [x] Correct the project tree and preserve local Go/Vite development commands as an optional workflow.

### Task 3: Update Operational References

**Files:**
- Modify: `QUICKREF.md`
- Modify: `MONITORING-QUICKREF.md`

- [x] Make the quick reference use the same Compose project name, service commands, authentication examples, Checker schedule, and data-safety warnings as `README.md`.
- [x] Make the monitoring reference clearly describe monitoring as an optional add-on, validate the script names, and use commands compatible with the current project.

### Task 4: Review Repository Files

**Files:**
- Review: `Dockerfile`, `docker-compose.yml`, `configs/`, `cmd/`, `internal/`, `migrations/`, `web/`, root scripts, Prometheus files, and Grafana JSON

- [x] Inspect configuration propagation, container lifecycle, migration behavior, authentication boundaries, Checker budgeting, and error handling using exact source references.
- [x] Search for secrets, placeholders, stale paths, unsafe destructive commands, and inconsistent versions.
- [x] Classify findings by severity and fix only documentation defects or narrow verification blockers within the approved scope.

### Task 5: Verify Submission Readiness

**Files:**
- Test: entire repository

- [x] Run `docker run --rm -v "${PWD}:/src" -w /src golang:1.26-alpine go test ./...` and require exit code 0.
- [x] Run `docker run --rm -v "${PWD}/web:/web" -w /web node:24-alpine sh -c 'npm ci && npm run build'` and require exit code 0.
- [x] Run `docker compose -p smart-router config`, build the application image, parse Grafana JSON, validate Prometheus files, and check available shell scripts.
- [x] Verify four running Smart Router containers, PostgreSQL/Redis health, Checker startup logs, balance records, and HTTP 200 responses from health and authenticated admin APIs.
- [x] Run `git diff --check`, inspect `git status --short`, and present all remaining findings and exact files for the user to commit. Do not commit or push.
