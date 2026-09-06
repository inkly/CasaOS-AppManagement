# CasaOS-AppManagement

The service that installs and runs apps on a CasaOS host. It is the Docker Compose layer of the system: it keeps the app store catalogues, turns a store entry into a `docker-compose.yml`, runs Compose against the local Docker daemon, and reports container status, logs and available updates back to the dashboard.

This repository is part of the **inkly distribution of CasaOS**, a maintained release of the project after upstream [IceWhaleTech](https://github.com/IceWhaleTech/CasaOS-AppManagement) stopped shipping in 2025. It descends from [alvins82's fork](https://github.com/alvins82/CasaOS-AppManagement), whose Docker SDK bump is what keeps CasaOS installable on a current daemon.

## What it does

The service listens on a random loopback port, writes that address to `/var/run/casaos/app-management.url`, and registers its paths with CasaOS-Gateway:

| Path | What is behind it |
|---|---|
| `/v2/app_management` | the current API |
| `/v1/apps`, `/v1/container`, `/v1/app-categories` | the v1 API, kept for older clients |
| `/doc/v2/app_management`, `/v1doc/v1/app_management` | the OpenAPI specs and their viewer |

Under `/v2/app_management`: `/appstore` registers and lists stores, `/apps` serves the merged catalogue (with `/apps/upgradable` and `/apps/{id}/compose`), `/categories` the category list and its counts, `/compose` and `/compose/{id}` install an app and then apply settings, update, start, stop or uninstall it (`/status`, `/containers`, `/logs`, `/healthcheck`), `/container/{id}` reports and health-checks a single container, and `/image` pulls images ahead of an install. `/compose/{id}/env` reads and replaces the `.env` next to that app's `docker-compose.yml` and re-creates the app so the new values apply. The full list is `api/app_management/openapi.yaml`; the Go server, types and MessageBus client are generated from it and are not committed.

Two jobs run on a schedule from `main.go`: the store catalogues are refreshed every 10 minutes, and every 15 seconds a sweep starts apps Docker abandoned at boot because their storage was not mounted yet.

On a host:

- `/etc/casaos/app-management.conf` — paths and the app store URLs, written from `build/sysroot/etc/casaos/app-management.conf.sample` on first start
- `/etc/casaos/env` — variables injected into every app
- `/var/lib/casaos/appstore` — the downloaded catalogues
- `/var/lib/casaos/apps/<name>/docker-compose.yml` — one directory per installed app; this is the file the settings page and the compose editor write
- `/var/log/casaos/app-management.log`
- `casaos-app-management.service`, started after `docker.service` and `casaos-message-bus.service`

There is no database. An installed app is its compose file plus whatever the Docker daemon reports about it.

## Install

Components are not installed on their own. The distribution is installed and upgraded with one command:

```sh
curl -fsSL https://github.com/inkly/CasaOS-Install/releases/latest/download/install.sh | sudo bash
```

What a release contains, and how it is built, is described in [CasaOS-Install](https://github.com/inkly/CasaOS-Install#readme).

## What this fork changed

From alvins82:

- **Docker 26 and later.** The SDKs were pinned to docker/cli and docker/docker v24, compose/v2 v2.23 and compose-go v1, which is why CasaOS stopped working against a current daemon. They are now v26.1.0, v2.27.0 and compose-go/v2 v2.1.0; the client already negotiated the API version with the daemon, and `pkg/docker/api_negotiation_test.go` now asserts it still does. This is the change that made the fork necessary. Offered upstream as [#217](https://github.com/IceWhaleTech/CasaOS-AppManagement/pull/217), still unmerged.
- **Apps abandoned at boot.** Docker gives up on a container whose bind mount does not exist yet, which happens on every reboot when apps live on a disk mounted after `docker.service`. A recovery sweep starts those apps once their storage appears, and leaves alone the ones the user stopped.

Here:

- **An app can keep its secrets in a `.env`.** An installed app may carry a `.env` next to its `docker-compose.yml`, edited from the dashboard through `GET`/`PUT /v2/app_management/compose/{id}/env`: `PUT` replaces the whole file (an empty body removes it) and re-creates the app so the new values apply. A reference to one of its keys (`${KEY}`, `$KEY`, `${KEY:-default}`), or to a key the runtime defines (`$AppID`, `${TZ}`, `$PUID`, `${PGID}`), now survives every settings round trip and the App Store update as written, where before the first save or update baked the resolved value into the compose file and every later `.env` edit was a no-op; the key has to be in `.env` when the compose is saved, or the reference is baked as a literal. It is kept in `environment` and every other free-text field (`image`, `command`, `labels`, `env_file`, `x-casaos`...) and in the host side of `ports` and `volumes`, which come back in the long syntax; a typed field such as `cpus`, `mem_limit` or `privileged` cannot hold one, and the editing load (`GET /compose/{id}` as YAML) fails with a `500` naming that field instead of serving the resolved value. A `.env` that defines a key the runtime sets itself (`TZ`, `PUID`, `PGID`, `AppID`, the environment of the CasaOS process) is refused with a `400` naming the key, since the runtime's value wins over the file anyway. When the compose file does not load or the pull fails after a `.env` change, the previous `.env` is put back together with the previous `docker-compose.yml`.
- **A `$` no longer doubles on every save.** The settings handler parsed the incoming YAML without interpolation and then re-escaped every `$`, so a value it had produced itself came back doubled: `$2a$12$…` became `$$2a$$12$$…`, then `$$$$2a$$$$12$$$$…`, and the container received a broken password hash. Parsing with interpolation on makes the round trip idempotent. Fixes [CasaOS#1988](https://github.com/IceWhaleTech/CasaOS/issues/1988).
- **Architecture filtering.** An app declaring `architectures: []` was hidden on every host, because an explicit empty list decodes to an empty slice rather than `nil` and only `nil` was treated as "runs anywhere" — which is what the dashboard already assumed. Category counts were computed from the unfiltered catalogue, so on arm hosts they were larger than the number of cards listed under them. Both now agree with the list. Supersedes the draft in [#202](https://github.com/IceWhaleTech/CasaOS-AppManagement/pull/202), which dereferenced that empty list and hardcoded amd64.
- **Releases.** The upstream workflow needed IceWhale's secrets and is gone. `.github/workflows/release.yml` runs the tests and publishes with GoReleaser on a `v*.*.*` tag using the default `GITHUB_TOKEN`. Our change to `.goreleaser.yaml` is one line, `release.github.owner`, now `inkly`, so the archive layout the installer consumes is unchanged.
- **The compose lifecycle test is opt-in.** It skipped itself only when no Docker daemon was reachable, which is the wrong question: a CI runner has a daemon and no CasaOS behind it, so the test ran and failed on the missing app store. It now requires `CASAOS_INTEGRATION`.

The compose editor in an app's settings is a dashboard feature. The endpoint it saves through, `PUT /v2/app_management/compose/{id}`, is upstream; what this fork changed behind it is the `$` handling and the `.env` references above.

## Development

```sh
go generate ./...
go build ./...
go test ./route/v2/ ./service/ -count=1
```

Go 1.21 or later, per `go.mod`. `codegen/` is generated and not committed, so `go generate` comes first; it fetches oapi-codegen and the MessageBus spec, so the first run needs network access.

`go test ./...` also runs `./pkg/docker`, which drives a real Docker daemon and pulls images from Docker Hub. On a Windows workstation those tests fail on a goroutine-leak assertion against a `go-winio` worker; CI runs them on Linux. `TestComposeAppLifecycle` in `./service` stays skipped unless `CASAOS_INTEGRATION` is set — it needs a live CasaOS host, not just a daemon.

## Licence

Apache License 2.0 — see [LICENSE](LICENSE); the upstream copyright notices are kept in the source. The service is the work of IceWhale and its contributors. The Docker SDK bump and the boot recovery sweep are [alvins82](https://github.com/alvins82)'s.
