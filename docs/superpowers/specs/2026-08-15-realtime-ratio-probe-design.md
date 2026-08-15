# Real-time Ratio Probe Design

## Goal

Let an operator measure the real price ratio of an upstream relay on demand from the Web console, show measured vs declared ratios per model, and feed fresh measurements into routing immediately.

## Definitions

- **Real ratio (实测倍率)**: `cost / total_tokens / base_price`, where base price is `$10/1M` tokens (same convention as the existing scheduled inference probe). A ratio of `2.0` means the upstream charges twice the baseline. The UI also shows the converted unit price `ratio × $10/1M` and the drift versus the declared ratio.
- **Declared ratio (声明倍率)**: the latest `/api/pricing` sync rows (`declared_prices.prompt_ratio` / `completion_ratio`), converted to `$10/1M`-baseline unit prices.
- **Scheduled probe**: the existing checker-run probe (`probe_interval`, default 1h) keeps its current behavior and model default; manual probes write to the same `probe_results` table with a distinct source.

## Architecture

### Backend

- `migrations/006_probe_source.up.sql`: add `source VARCHAR(20) DEFAULT 'scheduled'` to `probe_results` to distinguish `manual` from `scheduled` rows.
- `internal/checker/probe.go`: refactor `probeOne` to accept an explicit model, `max_tokens`, and `source`, and return a structured `ProbeResult` (real_ratio, cost, ttft, tokens, balances, stage). Add `ProbeModel` for on-demand calls. `ProbeChannel` (scheduler path) keeps its signature and behavior.
- `internal/api/ratio.go` (new): a `RatioHandler` with two endpoints:
  - `GET /admin/channels/:id/ratio` — declared table (latest per model), measured history (last 100 successful rows), and latest real ratio per model.
  - `POST /admin/channels/:id/probe-ratio` — runs one real inference with the chosen model. Flow: validate model against `model_mapping` → Redis lock (`SET NX EX 60`, key `ratio:probe:{channel}:{model}`; conflict → 409) → budget check (global daily spent from `probe_results`, channel effective budget = channel override > group min > global; exhausted → 429 with remaining budget) → probe with current epoch and `source='manual'` → invalidate the Redis snapshot cache so routing picks up the new ratio within the same request cycle.
- `internal/router/snapshot.go`: `loadProbeResults` selects the latest row per model by `checked_at` instead of `epoch`, so manual rows with the current epoch win over older scheduled rows; add `InvalidateSnapshotCache` to clear `router:snapshot` after a manual probe.
- `cmd/gateway/main.go`: construct a `checker.ProbeChecker` (with the configured `probe_model`), wire the `RatioHandler` and routes.

Error semantics: unsupported model → 400; probe in flight → 409; budget exhausted → 429; balance endpoint unavailable or chat failure → 502 with the failure stage and message; a zero-cost measurement (balance precision too low) is reported with a hint to raise `max_tokens`.

### Frontend

New 「倍率」 tab in the channel detail panel of `ChannelsView.vue`:

1. model selector (keys of the channel `model_mapping`, default first key) + `max_tokens` input (default 64) + 「立即实测」 button with loading state and duplicate-click guard
2. result card after a probe: real ratio, converted unit price, drift vs declared ratio, cost / tokens / TTFT / balance before→after
3. declared-ratio table (model, prompt/completion unit prices, synced at)
4. measured-history line chart for the selected model (tooltip shows source)
5. measured-record table (last 15: model, ratio, cost, tokens, TTFT, source, time)

## Verification

1. `go build ./... && go vet ./... && go test ./...` in a Go 1.26 container passes.
2. Migration 006 applies to the running database and existing rows default to `scheduled`.
3. `POST /admin/channels/:id/probe-ratio` returns 200 with a real ratio and writes a `source='manual'` row; the snapshot query then returns that row as the latest for the model.
4. Concurrency: a second concurrent request returns 409.
5. Budget: exhausting the channel or global budget returns 429 with remaining-budget info.
6. `GET /admin/channels/:id/ratio` returns declared/history/latest data; the Vue production build succeeds and the tab renders.

## Scope

This change adds on-demand measurement and display only. It does not change scheduling intervals, per-request traffic-derived ratio estimation, the `probe_model` default behavior, or the balance-check subsystem.
