# Full Checker Compose Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Package the existing full Checker in the Smart Router application image, run it as an independent Docker Compose service, and verify health, pricing, inference-probe, and balance scheduling without replacing the existing data volumes.

**Architecture:** Build `cmd/gateway` and `cmd/checker` into one Alpine runtime image while retaining `./gateway` as the image default. Add a portless `checker` Compose service that overrides the command to `./checker`, shares Gateway's database configuration and read-only config mount, and starts only after PostgreSQL and Redis are healthy.

**Tech Stack:** Go 1.26, multi-stage Docker build, Docker Compose v2, PostgreSQL 16, Redis 7, PowerShell verification commands

---

## File Structure

- Modify: `Dockerfile` - compile and copy both application entrypoint binaries into the runtime image.
- Modify: `docker-compose.yml` - declare the independently managed Checker service.
- Preserve: `configs/config.yaml` - keep the confirmed 30-second health, 10-minute pricing/balance, 1-hour probe, and USD 5 daily probe-budget settings unchanged.

### Task 1: Capture the Missing-Service Regression

**Files:**
- Test: `docker-compose.yml` via rendered Compose configuration

- [ ] **Step 1: Run the pre-change service assertion**

Run from the repository root:

```powershell
$services = @(docker compose -p smart-router config --services)
if ($LASTEXITCODE -ne 0) { throw 'docker compose config failed' }
if ($services -notcontains 'checker') { throw 'Expected failure: checker service is missing' }
```

Expected: FAIL with `Expected failure: checker service is missing`. This proves the test detects the current omission before any production file changes.

### Task 2: Package Both Go Programs

**Files:**
- Modify: `Dockerfile`
- Test: built runtime image filesystem

- [ ] **Step 1: Add the Checker build output**

Immediately after the existing Gateway build command, add:

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -o checker ./cmd/checker
```

- [ ] **Step 2: Copy the Checker into the runtime stage**

Immediately after copying `/build/gateway`, add:

```dockerfile
COPY --from=builder /build/checker .
```

Keep `CMD ["./gateway"]` unchanged so existing Gateway image behavior remains compatible.

- [ ] **Step 3: Build a temporary verification image**

Run:

```powershell
docker build -t smart-router-app:checker-verification .
```

Expected: exit code 0 and both Go build commands complete.

- [ ] **Step 4: Assert both entrypoints are executable**

Run:

```powershell
docker run --rm --entrypoint /bin/sh smart-router-app:checker-verification -c 'test -x /app/gateway && test -x /app/checker'
if ($LASTEXITCODE -ne 0) { throw 'runtime image does not contain both executable binaries' }
```

Expected: exit code 0 with no assertion output.

### Task 3: Add the Checker Compose Service

**Files:**
- Modify: `docker-compose.yml`
- Test: rendered Compose JSON

- [ ] **Step 1: Declare the Checker service**

Add this service after `gateway` and before the top-level `volumes` block:

```yaml
  checker:
    build: .
    container_name: smart-router-checker
    command: ["./checker"]
    environment:
      - DATABASE_HOST=postgres
      - DATABASE_PORT=5432
      - DATABASE_USER=gateway
      - DATABASE_PASSWORD=gateway_pass
      - DATABASE_NAME=smart_router
      - REDIS_HOST=redis
      - REDIS_PORT=6379
    volumes:
      - ./configs:/app/configs:ro
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    networks:
      - router_net
    restart: unless-stopped
```

Do not add ports or data volumes to this service.

- [ ] **Step 2: Render and parse the Compose model**

Run:

```powershell
$rendered = docker compose -p smart-router config --format json
if ($LASTEXITCODE -ne 0) { throw 'docker compose config failed' }
$compose = $rendered | ConvertFrom-Json
$checker = $compose.services.checker
if ($null -eq $checker) { throw 'checker service is missing' }
if (($checker.command -join ' ') -ne './checker') { throw 'checker command is incorrect' }
if ($checker.restart -ne 'unless-stopped') { throw 'checker restart policy is incorrect' }
if ($null -ne $checker.ports) { throw 'checker must not publish ports' }
if ($checker.depends_on.postgres.condition -ne 'service_healthy') { throw 'checker must wait for PostgreSQL health' }
if ($checker.depends_on.redis.condition -ne 'service_healthy') { throw 'checker must wait for Redis health' }
if ($null -eq $checker.networks.router_net) { throw 'checker is not attached to router_net' }
```

Expected: exit code 0 and no assertion throws.

- [ ] **Step 3: Commit the packaging and service definition**

Run:

```powershell
git add Dockerfile docker-compose.yml
git commit -m "feat: run checker with docker compose"
```

Expected: one commit containing only the two runtime-definition files.

### Task 4: Build and Start the Complete Stack

**Files:**
- Runtime only; preserve `smart-router_postgres_data` and `smart-router_redis_data`

- [ ] **Step 1: Confirm the existing named volumes before startup**

Run:

```powershell
docker volume inspect smart-router_postgres_data smart-router_redis_data --format '{{.Name}}'
```

Expected: both existing volume names are printed. Do not run `docker compose down -v` at any point.

- [ ] **Step 2: Rebuild and reconcile all four services**

Run:

```powershell
docker compose -p smart-router up -d --build
```

Expected: `smart-router-gateway`, `smart-router-checker`, `smart-router-db`, and `smart-router-redis` are running. Compose reuses the existing named volumes.

- [ ] **Step 3: Recheck the Compose-built image contents**

Run:

```powershell
docker compose -p smart-router run --rm --no-deps --entrypoint /bin/sh checker -c 'test -x /app/gateway && test -x /app/checker'
if ($LASTEXITCODE -ne 0) { throw 'Compose image does not contain both binaries' }
```

Expected: exit code 0.

### Task 5: Verify Checker Scheduling and Gateway Regression

**Files:**
- Test: Docker container state and logs
- Test: PostgreSQL `balance_checks` records
- Test: Gateway HTTP endpoints

- [ ] **Step 1: Verify container lifecycle and dependency health**

Run:

```powershell
$expected = @('smart-router-gateway', 'smart-router-checker', 'smart-router-db', 'smart-router-redis')
foreach ($name in $expected) {
  $state = docker inspect --format '{{.State.Status}}' $name
  if ($state -ne 'running') { throw "$name is not running: $state" }
}
foreach ($name in @('smart-router-db', 'smart-router-redis')) {
  $health = docker inspect --format '{{.State.Health.Status}}' $name
  if ($health -ne 'healthy') { throw "$name is not healthy: $health" }
}
docker compose -p smart-router ps
```

Expected: all four containers report `running`; PostgreSQL and Redis report `healthy`.

- [ ] **Step 2: Verify scheduler startup and the initial alive pass**

Run:

```powershell
$deadline = (Get-Date).AddSeconds(60)
do {
  $checkerLogs = docker compose -p smart-router logs --no-color --since 5m checker
  $ready = ($checkerLogs -match 'Scheduler started \(tick 5s\)') -and ($checkerLogs -match 'Initial alive check completed')
  if (-not $ready) { Start-Sleep -Seconds 3 }
} until ($ready -or (Get-Date) -ge $deadline)
if (-not $ready) { throw 'Checker scheduler did not complete initial alive checks within 60 seconds' }
$checkerLogs | Select-String 'Starting Health Checker|Scheduler started|Initial alive check completed'
```

Expected: logs include all three startup milestones.

- [ ] **Step 3: Verify the first balance attempt and report its actual outcome**

Run after at least one 5-second scheduler tick:

```powershell
$deadline = (Get-Date).AddSeconds(60)
do {
  $balanceRows = docker compose -p smart-router exec -T postgres psql -U gateway -d smart_router -At -c "SELECT channel_id || '|' || balance || '|' || currency || '|' || source || '|' || COALESCE(error, '') || '|' || checked_at FROM balance_checks ORDER BY checked_at DESC LIMIT 5;"
  if (-not $balanceRows) { Start-Sleep -Seconds 3 }
} until ($balanceRows -or (Get-Date) -ge $deadline)
if ($balanceRows) { $balanceRows } else { docker compose -p smart-router logs --no-color --since 5m checker | Select-String 'Balance check failed|balance' }
```

Expected: either a new `balance_checks` row is printed or Checker logs expose the exact upstream/configuration failure. A failed balance endpoint is reported, not treated as a scheduler startup failure.

- [ ] **Step 4: Verify existing Gateway APIs remain responsive**

Run:

```powershell
foreach ($path in @('/health', '/admin/groups', '/admin/channels')) {
  $response = Invoke-WebRequest -UseBasicParsing -Uri ("http://localhost:8080" + $path)
  if ($response.StatusCode -ne 200) { throw "$path returned $($response.StatusCode)" }
  "$path -> $($response.StatusCode)"
}
```

Expected: each endpoint returns HTTP 200.

- [ ] **Step 5: Run final repository and runtime evidence checks**

Run:

```powershell
git status --short
docker compose -p smart-router ps
docker compose -p smart-router logs --no-color --tail 100 checker
```

Expected: no uncommitted implementation files, four running services, and no fatal Checker exit loop. Record any upstream balance or probe warning verbatim in the handoff.
