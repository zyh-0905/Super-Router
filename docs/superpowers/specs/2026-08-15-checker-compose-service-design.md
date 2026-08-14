# Checker Compose Service Design

## Goal

Run the existing full Checker under Docker Compose so that health, pricing, inference probe, and balance checks start with the rest of Smart Router and recover automatically after failures or host restarts.

## Architecture

Use one application image containing both `gateway` and `checker` binaries. Keep Gateway and Checker as separate Compose services so Docker manages their lifecycles independently. The Checker service will override the image command with `./checker`, use the same configuration and database environment as Gateway, and wait for PostgreSQL and Redis health checks before starting.

The Checker exposes no host port. It reads channel configuration from PostgreSQL, performs scheduled upstream checks, and writes results back to the existing health, pricing, probe, and balance tables. Gateway continues to expose those results through its existing admin APIs and Web UI.

## Image Changes

The Go build stage in `Dockerfile` will compile both programs:

- `/build/gateway` from `./cmd/gateway`
- `/build/checker` from `./cmd/checker`

The runtime stage will copy both binaries. Its default command remains `./gateway`, preserving the current Gateway behavior. Compose will explicitly run `./checker` for the Checker service.

## Compose Service

Add a `checker` service with:

- image built from the existing `Dockerfile`
- container name `smart-router-checker`
- command `./checker`
- the same PostgreSQL and Redis environment variables as Gateway
- the existing read-only `./configs:/app/configs` mount
- healthy PostgreSQL and Redis dependencies
- the shared `router_net` network
- restart policy `unless-stopped`

No new volumes, ports, credentials, or databases are introduced.

## Scheduling And Cost

The existing configuration remains authoritative:

- alive checks: every 30 seconds
- pricing synchronization: every 10 minutes
- balance checks: every 10 minutes, with the first attempt on the first scheduler tick
- inference probes: every hour
- global daily inference-probe budget: USD 5
- per-channel and per-group effective budgets continue to apply

Starting the full Checker authorizes these scheduled inference probes and their possible upstream charges within the configured budgets.

## Failure Handling

If PostgreSQL or Redis is not healthy, Compose will defer Checker startup. If Checker exits later, Docker restarts it. Individual upstream check failures are logged by the existing Checker code and do not terminate the scheduler. An unsupported or misconfigured balance endpoint may produce a failed balance record or no usable balance value; it does not prevent health, pricing, or probe jobs from running.

## Verification

Implementation is accepted when all of the following are demonstrated with fresh commands:

1. The pre-change Compose assertion fails because no Checker service exists.
2. `docker compose config` includes a Checker service with the expected command, dependencies, environment, network, and restart policy.
3. The rebuilt application image contains both `/app/gateway` and `/app/checker`.
4. Gateway, Checker, PostgreSQL, and Redis are running; PostgreSQL and Redis are healthy.
5. Checker logs show scheduler startup and an initial alive check.
6. Within the initial scheduling window, the balance task attempts the configured channel and the database or logs show its outcome.
7. Existing Gateway health and admin APIs remain responsive.

## Scope

This change only packages and starts the existing Checker. It does not alter scheduling logic, probe budgets, balance protocol detection, channel credentials, database schema, or the Web UI.
