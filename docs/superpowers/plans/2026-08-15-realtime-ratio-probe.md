# Real-time Ratio Probe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add on-demand real-ratio measurement for upstream relays (Web 「倍率」 tab), display declared vs measured ratios, and feed fresh measurements into routing.

**Architecture:** Manual probes run inside the Gateway (the Checker has no HTTP port), reusing `checker.ProbeChecker`. Results go to the existing `probe_results` table with a new `source` column; the router snapshot picks the latest row per model by `checked_at`. Redis provides a per-channel/model concurrency lock; the existing daily probe budgets protect spend.

**Tech Stack:** Go 1.26, Gin, pgx, Redis, PostgreSQL 16, Vue 3 + Vite, Docker Compose, PowerShell verification.

---

## File Structure

- Add: `migrations/006_probe_source.up.sql` / `.down.sql`
- Add: `internal/api/ratio.go`
- Add: `internal/api/ratio_test.go` (pure helpers)
- Modify: `internal/checker/probe.go`, `internal/router/snapshot.go`, `cmd/gateway/main.go`
- Modify: `web/src/api.js`, `web/src/views/ChannelsView.vue`
- Add: `docs/superpowers/specs/2026-08-15-realtime-ratio-probe-design.md`, this plan

### Task 1: Migration 006

- [ ] **Step 1: Write migration files**

`006_probe_source.up.sql`:

```sql
-- 006_probe_source: 区分手动/定时探测来源
ALTER TABLE probe_results ADD COLUMN source VARCHAR(20) DEFAULT 'scheduled';
```

`006_probe_source.down.sql`:

```sql
ALTER TABLE probe_results DROP COLUMN source;
```

- [ ] **Step 2: Apply to the running database (existing volume does not auto-run migrations)**

```powershell
Get-Content migrations/006_probe_source.up.sql | docker exec -i smart-router-db psql -U gateway -d smart_router
```

Expected: `ALTER TABLE` and existing rows report `source='scheduled'`.

### Task 2: Checker Probe Refactor

- [ ] **Step 1: Structured result + parameterized probeOne**

Add a `ProbeResult` struct and change `probeOne` to `probeOne(ctx, upstream, epoch, model, maxTokens, source) (*ProbeResult, error)`. The failure row insert includes `source`; the success insert includes `source`. Keep balance-before/after errors as returns (no row).

- [ ] **Step 2: Public on-demand entry**

Add `ProbeModel(ctx, upstream, epoch, model, maxTokens, source) (*ProbeResult, error)`. Keep `ProbeChannel(ctx, upstream, epoch) (float64, error)` delegating with `p.probeModel`, 16 tokens, `scheduled` (scheduler compatibility). Update the legacy `Run` call site.

- [ ] **Step 3: callChatCompletion maxTokens parameter**

`callChatCompletion(ctx, upstream, model, maxTokens)` builds the request with the given token cap.

### Task 3: Snapshot Latest-by-time and Cache Invalidation

- [ ] **Step 1: loadProbeResults order by checked_at**

Change `ORDER BY model, epoch DESC` to `ORDER BY model, checked_at DESC` (still `DISTINCT ON (model)`).

- [ ] **Step 2: InvalidateSnapshotCache**

Export `InvalidateSnapshotCache(ctx, redis)` in `snapshot.go` deleting `router:snapshot`.

### Task 4: Ratio Handler

- [ ] **Step 1: Write internal/api/ratio.go**

`RatioHandler{db, redis, cfg, probe, logger}` with `GetRatio` and `ProbeRatio`:

- Load channel `Upstream` + `model_mapping`; validate the requested model key (400).
- `max_tokens` default 64, cap 256.
- Redis lock `ratio:probe:{id}:{model}` NX EX 60 (409 on failure; always release with defer).
- Budget: `probe.TodaySpent` vs `cfg.Checker.DailyProbeBudget` (global); `probe.UpstreamTodaySpent` vs effective channel budget from `checker.LoadSchedules` lookup (fallback global for disabled channels). Exhausted → 429 with `remaining_budget`.
- Run `probe.ProbeModel(..., "manual")` at the current epoch; on success `router.InvalidateSnapshotCache`.
- Fetch the latest declared row for the model; compute drift against the token-weighted declared blend (fallback prompt_ratio).

- [ ] **Step 2: Gateway wiring**

In `cmd/gateway/main.go` construct the probe checker (with `SetProbeModel(cfg.Checker.ProbeModel)`), construct `RatioHandler`, register `GET /admin/channels/:id/ratio` and `POST /admin/channels/:id/probe-ratio`.

- [ ] **Step 3: Unit tests**

`internal/api/ratio_test.go`: drift computation helper (blend vs fallback), max-tokens clamping, model-key validation helper. Run the full Go test suite in the golang:1.26-alpine container.

### Task 5: Frontend

- [ ] **Step 1: api.js**

Add `channelRatio(id)` (GET) and `probeRatio(id, model, maxTokens)` (POST).

- [ ] **Step 2: ChannelsView 「倍率」 tab**

New tab entry `{k:'ratio',l:'倍率'}` + `loadRatio` on tab click; state `ratio`, `ratioLoading`, `probing`, `probeModel`, `probeTokens`, `probeResult`. Content: probe controls row; result card (ratio, unit price, drift, cost/tokens/TTFT/balance); declared table; history chart (BaseChart, selected model only); record table. Reset ratio state in `select()`.

- [ ] **Step 3: Production build**

`npm run build` inside the web directory (or rely on the Docker image build).

### Task 6: Deploy and Verify Live

- [ ] **Step 1: Rebuild and restart Gateway and Checker**

```powershell
docker compose -p smart-router up -d --build gateway checker
```

- [ ] **Step 2: On-demand probe**

`POST /admin/channels/1/probe-ratio {"model":"gpt-5.5"}` → 200 with `real_ratio`; verify `probe_results` row `source='manual'`.

- [ ] **Step 3: Ratio endpoint**

`GET /admin/channels/1/ratio` returns declared/history/latest.

- [ ] **Step 4: Concurrency and budget guards**

Second overlapping probe → 409; temporarily exhausted budget → 429; restore test data (budget setting / circuit rows) afterward.

- [ ] **Step 5: Final checks**

`git status --short`, container health, gateway logs free of unexpected errors.
