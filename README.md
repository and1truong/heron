# Heron

![](assets/hero.png)

Lazy-start HTTP/gRPC/TCP proxy and local process supervisor for development services.

`heron` keeps local services stopped until they are needed. The first request to a configured route starts the target service, waits for it to become ready, proxies the request, and shuts the service down again after it has been idle for a configured period.

Written in Go.

## Why

Local development environments often require many services, but most of them do not need to run continuously.

`heron` gives you one stable local endpoint while starting individual services only when traffic arrives.

```text
Client
  |
  v
localhost:3000
  |
  +-- /service/api/* ----> localhost:1980
  |
  +-- /service/web/* ----> localhost:1981
  |
  +-- /service/admin/* --> localhost:1982
```

Services remain stopped until requested.

## Features

* Lazy-start services on first request
* Reverse proxy HTTP traffic by path or hostname
* Proxy native gRPC over HTTP/2 cleartext (h2c)
* Proxy raw TCP traffic on per-service listener ports
* Automatic idle shutdown
* Per-service idle timeout
* Per-service environment variables
* Optional build command before launch
* Configurable working directory
* Optional custom stop command
* Automatic process termination when no stop command exists
* Readiness detection before proxying traffic
* Concurrent startup deduplication
* Child process stdout/stderr forwarding
* Per-service log prefixes
* Configurable log levels
* Config doctor for validating every configured service
* Fixed-interval process-wide scheduled tasks
* Graceful shutdown on `SIGINT` / `SIGTERM`
* Minimal dependencies

## Installation

```bash
go install github.com/and1truong/heron/cmd/heron@latest
```

Or build from source:

```bash
git clone https://github.com/and1truong/heron.git
cd heron
go build -o heron ./cmd/heron
```

## Releases

Pushing a semantic-version tag creates a GitHub release with checksums and archives for macOS, Linux, and Windows on AMD64 and ARM64:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs the test suite and `go vet`, then GoReleaser publishes the generated artifacts to [GitHub Releases](https://github.com/and1truong/heron/releases).

## Usage

Show available commands, options, and examples:

```bash
heron help
heron help doctor
```

`-h` and `--help` are also supported, including `heron doctor --help`.


```bash
heron -c /path/to/config.yaml
```

Validate the configuration and inspect every resolved app without running any
hooks or app commands:

```bash
heron doctor -c /path/to/config.yaml
```

`doctor` rejects unknown fields and checks the routing, protocol, port,
working-directory, environment-file, and interpolation rules used at startup.
It prints apps in a stable order with their public endpoint and backend target,
and exits non-zero when validation fails.

Without `-c`, `heron` uses:

```text
~/.config/heron.yaml
```

## Configuration

```yaml
# Optional: compose app definitions from other YAML files.
resources:
  - apps/backend.yaml
  - apps/frontend.yaml

port: 3000
idle: 30m
startTimeout: 30s
stopTimeout: 10s
logLevel: info

startUp:
  - /path/to/custom/start-command
tearDown:
  - /path/to/custom/cleanup-command

scheduledTasks:
  cleanup:
    command: /path/to/custom/cleanup-script
    every: 1h
    timeout: 5m
    runOnStart: false
    overlap: skip

apps:
  SERVICE_ID:
    pwd: /path/to/dir
    build: /command/to/build/it
    launch: /command/to/start/it
    stop: /optional/command/to/stop/it
    env:
      APP_ENV: development
      LOG_LEVEL: debug
    protocol: http # http (default), grpc, or tcp
    # Optional HTTP routing override. If both are omitted, host defaults to
    # SERVICE_ID.localhost.
    # path: /service/SERVICE_ID
    # host: custom.localhost
    # TCP services use listenPort instead of path or host:
    # listenPort: 11980
    port: 1980
    # Optional for gRPC services. Wait for the standard health service to
    # report SERVING instead of using TCP port readiness.
    # health:
    #   grpc: true
    idle: 5m # set to 0 to disable automatic idle shutdown
    includePrefix: false # path routing only
```

### Global options

| Option           | Description                                        | Default |
| ---------------- | -------------------------------------------------- | ------- |
| `port`           | Port `heron` listens on                            | `3000`  |
| `idle`           | Default service idle timeout                       | `30m`   |
| `startTimeout`   | Maximum time to wait for a service to become ready | `30s`   |
| `stopTimeout`    | Maximum time allowed for service shutdown          | `10s`   |
| `logLevel`       | `debug`, `info`, `warning`, or `error`             | `info`  |
| `startUp`        | Commands run sequentially before traffic is served | `[]`    |
| `tearDown`       | Best-effort commands run sequentially on shutdown  | `[]`    |
| `scheduledTasks` | Named fixed-interval commands                      | `{}`    |

### Composing configuration files

Use `resources` to split app definitions across local YAML files:

```yaml
# heron.yaml
port: 3000
idle: 30m
resources:
  - apps/backend.yaml
  - infrastructure.yaml
```

```yaml
# apps/backend.yaml
apps:
  api:
    pwd: ~/code/api
    launch: go run ./cmd/api
    port: 1980
```

Resource files may recursively declare their own `resources`. Paths resolve
relative to the file that declares them. Resource files may contain only
`resources` and `apps`; process-wide settings remain in the root configuration.

App IDs must be unique across the complete resource graph. Duplicate IDs and
circular includes are rejected with the relevant source files. Resources are
composition only: their order does not imply merge or override behavior. V1
supports local YAML files, not URLs, globs, directories, inheritance, or patches.

`startUp` and `tearDown` are process-wide hooks. Each command uses the platform shell and inherits heron's working directory and environment, matching service command execution. A failed `startUp` command aborts startup. During shutdown, every `tearDown` command is attempted even when an earlier command fails.

### Scheduled tasks

`scheduledTasks` starts after `startUp` succeeds and runs only while Heron is
active. Each task requires a `command` and positive `every` duration. `timeout`
defaults to `5m`; `runOnStart` defaults to `false`; and `overlap` defaults to
`skip`, the only policy supported in this version.

A task normally waits one full interval before its first run. With
`runOnStart: true`, it also runs immediately. If the previous invocation is
still active at the next tick, that tick is skipped. Failures are logged and
later runs continue. Heron does not persist schedules or catch up missed runs.
On shutdown it stops the scheduler, cancels and joins active task processes,
then runs `tearDown`. Commands inherit Heron's working directory and
environment.

### Service options

| Option          | Description                                                    |
| --------------- | -------------------------------------------------------------- |
| `pwd`           | Working directory for build, launch, and stop commands         |
| `build`         | Optional command executed before starting the service          |
| `launch`        | Command used to start the service                              |
| `stop`          | Optional command used to stop the service                      |
| `envFiles`      | Ordered dotenv files; relative paths resolve against `pwd` |
| `env`           | Environment variables for build, launch, and stop commands; app values override inherited variables |
| `protocol`      | Proxy protocol: `http` (default), `grpc`, or `tcp`               |
| `path`          | Optional public path prefix; mutually exclusive with `host`              |
| `host`          | Exact hostname; defaults to `<SERVICE_ID>.localhost` when `path` is omitted |
| `listenPort`    | Public local listener port; required for TCP services           |
| `port`          | Local port used by the service                                 |
| `idle`          | Overrides the global idle timeout; `0` disables idle shutdown  |
| `includePrefix` | Whether the configured path prefix is preserved; path routing only |
| `health.grpc`   | For gRPC, wait for the standard health service to report `SERVING` |
| `endpoints`     | Named ports exposed by one application; cannot be combined with legacy routing fields |

Applications that only need process supervision may omit both `port` and
`endpoints`. They can be started and stopped from the TUI but do not receive a
proxy route.

### Multiple endpoints

Use named endpoints when one process exposes more than one port:

```yaml
apps:
  foo:
    pwd: ~/code/foo
    launch: ./foo
    endpoints:
      web:
        port: 8080
        protocol: http
        primary: true
      metrics:
        port: 9090
        protocol: http
```

The primary endpoint receives both the short application host and its named
alias. Other endpoints use hierarchical lazy hosts:

| Endpoint | Lazy host |
| --- | --- |
| Primary `web` | `foo.localhost` and `web.foo.localhost` |
| `metrics` | `metrics.foo.localhost` |

A single named endpoint becomes primary automatically. Two or more endpoints
must mark exactly one endpoint with `primary: true`. Endpoint names are
case-insensitive DNS labels. Every endpoint must declare a unique backend
`port`; TCP endpoints must also declare a unique public `listenPort`.

HTTP, gRPC, and TCP endpoint objects accept the same routing, health, and
protocol fields as the legacy single-port form. Requests and connections across
all endpoints share one application lifecycle: the first request starts the
process once, and activity on any endpoint prevents idle shutdown. Startup is
complete after every configured endpoint passes readiness.

Existing single-port configurations remain supported and behave as an implicit
primary endpoint named `default`.

## Environment files and interpolation

```yaml
apps:
  appFoo:
    pwd: /path/to/project
    launch: npm run dev
    port: 1980
    envFiles:
      - .env
      - .env.local
    env:
      foo: "include dynamic value: {{FOO:default-foo-value}}"
      API_URL: "{{API_URL:http://localhost:8080}}"
```

Files are loaded in order; later files override earlier files. Inline `env`
overrides files, which override inherited process variables. Relative file paths
resolve against the service's `pwd`; absolute paths and `~/` are supported.
Missing, unreadable, or malformed files fail configuration loading.

Inline `env` values support multiple `{{NAME}}` and `{{NAME:default}}` expressions.
Lookups use inherited variables overlaid with the service's files. Inline entries
do not reference each other. Defaults apply only to unset variables; an explicitly
empty value remains empty. Missing variables without defaults and malformed
expressions fail configuration loading. Defaults may contain colons. Substituted
values are not recursively expanded; `${NAME}` remains literal.

Files support `KEY=value`, optional `export`, blank lines, comments,
and single/double quotes. Quoted values may span multiple lines; newlines and
whitespace inside the quotes are preserved (CRLF line endings become LF). Double quotes support `\n`, `\r`, `\t`, `\\`, and `\"`.
Unquoted inline comments begin with `#` following whitespace. Inside quotes,
`#` and assignment-like lines are value content. As a Heron extension for pasted
text, smart single-quote delimiters `‘…’` and `‘…‘` are also accepted and treated
like literal single quotes; inner Unicode characters are not rewritten.
File values are literal: no shell execution or variable expansion is performed.

```dotenv
FOO='
{ "foo": "bar" }
'
```

This sets `FOO` to the JSON text with a leading and trailing newline.

Files and templates are resolved once when the configuration loads. Restart
heron to pick up changes. The resolved environment is shared by the service's
build, launch, and stop commands, without modifying the parent environment or
other services.

## Example

Configuration:

```yaml
port: 3000
idle: 30m

apps:
  api:
    pwd: ~/code/my-api
    launch: go run ./cmd/api
    env:
      APP_ENV: development
      LOG_LEVEL: debug
    path: /service/api
    port: 1980
    idle: 5m
    includePrefix: false
```

Start `heron`:

```bash
heron
```

Then request:

```bash
curl http://localhost:3000/service/api/hello
```

If `api` is stopped, `heron`:

1. Starts the service.
2. Waits for `localhost:1980` to accept connections.
3. Proxies the request to:

```text
http://localhost:1980/hello
```

4. Keeps the service running while it receives traffic.
5. Stops it after five minutes without requests.

## Host routing

HTTP services default to host routing when neither `host` nor `path` is configured. The app ID becomes `<SERVICE_ID>.localhost`:

```yaml
apps:
  docs:
    pwd: ~/code/docs
    launch: npm run dev
    port: 1988
```

This exposes `docs` at `http://docs.localhost:3000`. You can also configure an exact hostname explicitly:

```yaml
apps:
  docs:
    pwd: ~/code/docs
    launch: npm run dev
    host: docs.localhost
    port: 1988
```

With wrapper port `3000`, `http://docs.localhost:3000/guide?q=1` is proxied to `http://127.0.0.1:1988/guide?q=1`. The path and query string are unchanged. Host matching is case-insensitive and ignores the wrapper port; it does not perform wildcard or suffix matching. `includePrefix` is not valid for host-routed services.

Backends receive `Host: 127.0.0.1:<service-port>` so they identify the local target consistently; the incoming hostname is retained in `X-Forwarded-Host`.

Heron listens on IPv4 loopback and, when available, IPv6 loopback. Nested
`.localhost` names such as `metrics.foo.localhost` therefore require no hosts
file entries on platforms and clients implementing the reserved `.localhost`
namespace, including current macOS browsers.

## gRPC proxying

Use `protocol: grpc` for native gRPC services:

```yaml
apps:
  greeter:
    pwd: ~/code/greeter
    launch: go run ./cmd/server
    protocol: grpc
    host: greeter.localhost
    port: 50051
    idle: 5m
    health:
      grpc: true
```

Connect a plaintext gRPC client to `greeter.localhost:3000`. heron accepts HTTP/2 cleartext (h2c), starts the backend on the first RPC, waits for readiness, and proxies to `127.0.0.1:50051` without protobuf descriptors. Unary, client-streaming, server-streaming, bidirectional-streaming, metadata, deadlines, cancellation, status details, and trailers pass through transparently.

An open RPC or stream keeps the service active. The idle timer starts only after the final RPC closes. gRPC services use hostname routing and do not support `path`, `includePrefix`, or `listenPort`.

When `health.grpc` is enabled, the backend must implement the [standard gRPC health checking protocol](https://github.com/grpc/grpc/blob/master/doc/health-checking.md) and report `SERVING` for the overall server (`service: ""`). Without it, heron considers the service ready when its TCP port accepts connections.

The initial implementation is local-development oriented: plaintext h2c only. TLS termination, TLS upstreams, and gRPC-Web translation are not currently supported.

## TCP proxying

Use `protocol: tcp` for PostgreSQL, Redis, MySQL, SSH, and other local TCP services:

```yaml
apps:
  postgres:
    pwd: ~/code/my-project
    launch: docker compose up postgres
    stop: docker compose stop postgres
    protocol: tcp
    listenPort: 15432
    port: 5432
    idle: 30m
```

Connect through the heron listener:

```bash
psql postgres://user:password@127.0.0.1:15432/database
```

The first connection starts the service and waits for its target port to become ready. TCP bytes are then copied bidirectionally without protocol-specific processing. The service remains active while any connection is open; the idle timer begins only after the final connection closes. Long-lived connection pools therefore keep the service running.

TCP services require a unique `listenPort`. They do not support `path`, `host`, or `includePrefix` routing.

### Single-broker Kafka

A local single-broker Kafka instance works through the generic TCP proxy. Kafka must advertise the heron listener, not its backend listener, because clients reconnect to the broker address returned in Kafka metadata.

Configure heron:

```yaml
apps:
  kafka:
    pwd: ~/dev/kafka
    launch: bin/kafka-server-start.sh config/server.properties
    protocol: tcp
    listenPort: 19092
    port: 9092
    idle: 0
```

Configure the Kafka broker:

```properties
listeners=PLAINTEXT://127.0.0.1:9092
advertised.listeners=PLAINTEXT://127.0.0.1:19092
```

Configure clients:

```properties
bootstrap.servers=127.0.0.1:19092
```

Use `idle: 0` for Kafka. Producers and consumers commonly keep connections open and automatically reconnect; an idle shutdown would otherwise be ineffective or create a stop/reconnect/start loop.

This setup intentionally supports localhost and one broker. It does not parse Kafka frames or rewrite metadata. Multi-broker clusters require a separate proxied listener and advertised address for every broker.

To run the opt-in integration test against a local Kafka distribution:

```bash
KAFKA_HOME=/path/to/kafka go test -tags=integration ./integration
```

The test formats an isolated single-node KRaft data directory, starts Kafka lazily through heron, and verifies topic administration plus a producer/consumer round trip using Kafka's CLI clients.

## Path forwarding

Given:

```yaml
path: /service/api
port: 1980
includePrefix: false
```

This request:

```text
GET http://localhost:3000/service/api/users
```

becomes:

```text
GET http://localhost:1980/users
```

With:

```yaml
includePrefix: true
```

the same request becomes:

```text
GET http://localhost:1980/service/api/users
```

HTTP methods, headers, query parameters, request bodies, response status, and response bodies are preserved.

## Service lifecycle

```text
stopped
   |
   v
building
   |
   v
starting
   |
   v
running
   |
   v
stopping
   |
   v
stopped
```

A failed build or startup transitions the service into a failure state and returns an error to the triggering request.

Concurrent requests do not cause duplicate service launches. Requests arriving while a service is starting wait for the same startup operation.

## Idle shutdown

Every successful request updates the service's activity time.

A service is eligible for shutdown when:

```text
time since last activity >= idle timeout
AND
active requests == 0
```

This prevents Heron from stopping a service while it is still handling traffic.

## Process management

If `stop` is configured, Heron executes it when the service needs to shut down.

Otherwise, Heron terminates the process it started.

Graceful termination is attempted first. A process that does not exit within the shutdown timeout may be forcefully terminated.

## Logging

Example:

```text
INFO  [api] starting service
INFO  [api] stdout: listening on :1980
INFO  [api] service ready
DEBUG [api] GET /service/api/users -> http://localhost:1980/users
INFO  [api] idle timeout reached
INFO  [api] stopping service
```

Child process stdout and stderr are forwarded through Heron with the service name attached.

Supported levels:

```text
debug
info
warning
error
```

## Graceful shutdown

When Heron receives `SIGINT` or `SIGTERM`, it:

1. Stops accepting new work.
2. Gracefully shuts down the HTTP server.
3. Stops managed services.
4. Waits for cleanup.
5. Exits.

## Use cases

Heron is useful for:

* local microservice development
* rarely used development tools
* AI/agent development environments
* local dashboards and admin services
* reducing CPU and memory use from idle services
* providing stable local URLs for dynamically started applications

## Status

Early development.

The initial goal is intentionally small:

> One process, one configuration file, one public port, and local services that wake on demand.

## License

MIT


## Interactive TUI

```sh
heron                    # normal proxy/supervisor, plain log output
heron tui                # opt-in interactive supervisor
heron tui -c ./config.yaml
heron help tui
```

Both modes run the same proxy and supervisor. Apps remain lazy until traffic or
an explicit Start. TUI requires an interactive stdin/stdout on Linux or macOS;
redirected input/output is rejected before hooks or listeners start. It starts
its own supervisor; it does not attach to an existing Heron instance.

The left pane lists managed apps. The right pane shows overview/process tree
above logs or lifecycle events. Small terminals show a resize hint.

| Key | Action |
| --- | --- |
| Arrows / Tab | Select or scroll / change focused pane |
| `1`, `2`, `3` | Maximize apps, processes, or logs; repeat to restore split |
| `p`, Enter | Focus process tree / expand or collapse tree |
| `l`, `e` | Show app output or lifecycle events |
| `/`, Enter | Filter apps or focused logs/events / apply |
| PgUp, PgDn, `g` | Scroll output / resume tail |
| `S`, `x`, `r` | Start, stop, restart selected app |
| `k` | Force kill with confirmation pinned to selected app |
| `?` | Help |
| `q`, Ctrl-C | Confirm quit / immediate quit; both shut down managed apps |

Manual Stop blocks new lazy starts until Start/Restart. Existing requests may be
interrupted by manual actions. Start respects the app's configured idle timeout;
opening TUI does not keep apps awake. Force kill is unavailable for apps with a
custom `stop` command because the launcher may not own the external workload.

CPU/RSS sampling runs roughly once per second using OS `ps`, outside rendering.
CPU uses deltas in cumulative process CPU time: 100% equals one CPU core, so
multi-core workloads can exceed 100%. Initial samples establish a baseline;
`ps` time precision may make short bursts appear coarse. Each process baseline
is keyed by PID and OS start time. RSS is summed across owned processes and may
double-count shared pages; VMS is available in process details. The latest 120
CPU/RSS samples are kept per app. Logs and events are separate in-memory buffers,
each capped at 500 entries per app and 4 KiB per entry. Nothing is persisted.

Ownership uses the process group established at launch, including reparented
members. A live launch-group leader is required to anchor each snapshot.
Processes that create new sessions/groups, containers, and external daemons
cannot be inferred reliably; their metrics are unavailable rather than reported
as zero. The process tree preserves PID/start-time selection across refreshes;
large trees initially collapse. Collection failures leave lifecycle controls
available. Terminal control characters in app names/log output are sanitized.


## App dependencies

Use `dependsOn` to keep dependencies available throughout an app's lifetime:

```yaml
apps:
  postgres:
    pwd: /srv/project
    launch: postgres -D ./data
    port: 5432
    protocol: tcp
    listenPort: 15432
    idle: 5m
  api:
    pwd: /srv/project
    launch: ./api
    port: 8080
    dependsOn: [postgres]
```

Dependencies become ready before the dependent builds or launches. Each running
app holds a lease on its direct dependencies, including between requests. Shared
and transitive dependencies are supported; concurrent starts reuse one startup.
Unknown IDs, self-dependencies, duplicate dependencies, and cycles are rejected.

A dependency can idle-stop only after all dependent leases and active requests
are released. Releasing the last lease starts its normal idle interval (`idle: 0`
still disables idle shutdown). Failed or cancelled startup and unexpected process
exit release the app's leases. Cancelling an individual request does not cancel
shared startup. Apps using a custom `stop` command remain externally managed;
Heron cannot detect the daemon exiting through its launcher process.

Manual stop, kill, and restart reject active dependents and name the blockers.
Stop those dependents first. TUI inspection shows dependencies and “Kept alive by”.
Global shutdown blocks new starts and stops dependents before dependencies. If a
custom stop command fails, dependency leases remain held because the app may still
be running; retry stop before restarting it. Dependency crashes are not cascaded
into automatic restarts or stops of their dependents.


### Debug a single app

```sh
heron --app=api
heron tui --app=api
heron -c ./config.yaml --app api
```

`--app` selects an exact key from `apps` and immediately starts it together with
all transitive `dependsOn` dependencies, waiting for dependency readiness first.
Only these apps have proxy routes, app port checks, TCP listeners, and TUI entries.
Global scheduled tasks are disabled in this mode; global `startUp`/`tearDown`
hooks and the shared HTTP/gRPC listener remain active. Configuration validation
still covers the complete configuration.

Startup failures exit the non-interactive command with an error after cleanup.
The TUI stays open to inspect logs/events and retry with its Start action.
Existing idle timeouts and lifecycle controls still apply. Unknown or empty app
names fail before hooks, processes, or listeners start. Omit `--app` for normal
lazy startup of the full configuration. The flag is not supported by `doctor`.

## Graphical UI

```sh
heron ui -c ./config.yaml
heron ui --app api
heron help ui
```

Open the printed `http://127.0.0.1:<port>` address. The UI is embedded in the Go
binary: no Node runtime, frontend build, or additional dependencies are required.
Like TUI mode, this starts its own supervisor and proxy; it does not attach to an
existing instance. Without `--app`, applications remain lazy until traffic or an
explicit Start. Closing a browser tab leaves Heron running; Ctrl-C in the terminal
shuts down Heron and its managed apps.

The Applications view provides searchable state filters, process-group CPU/RSS,
multiple endpoints, an app inspector, and dependency navigation. Each HTTP endpoint
has its own link in the table and inspector, opening in a new browser tab through
Heron's proxy (including its configured port and path). Links remain available for
stopped apps so lazy startup still works. The primary endpoint is marked in the
inspector; gRPC/TCP endpoints show client addresses instead of browser links. Stop and Restart
are disabled while active dependents hold leases; the supervisor enforces the
same restriction for API requests. Pending operations and failures are visible.
External/custom-stop workloads report metrics as unavailable.

The bottom log pane is resizable and collapsible. **Expand logs** opens the same
pane in a large modal, retaining its filter, autoscroll, and clear-view state.
Use **Close expanded logs** or Escape to return to the previous docked size and
collapsed state. Cmd/Ctrl-K focuses the log filter while the modal is open. Disable autoscroll (or scroll
up) to freeze the displayed history; **Return to live** resumes the bounded stream.
**Clear view** only hides current entries in that browser. Activity shows separate
app and runtime lifecycle events. Logs/events retain at most 500 entries per app.
Use Cmd/Ctrl-K to focus app search; controls support normal keyboard navigation.

**Add app** and **Configuration** edit one application's YAML in its original
source file, including composed resource files. Configuration is hidden until
explicitly revealed because it can contain credentials. Save validates the entire
resource/dependency graph without running hooks or commands, preserves unrelated
YAML nodes and file permissions, and rejects stale edits. Restart Heron to apply
saved changes; saving does not change the running supervisor. YAML whitespace may
be normalized. Concurrent external editors should still be avoided during save:
the version check catches observed changes but is not an OS-wide file lock.

The UI uses a separate ephemeral IPv4 loopback listener, validates Host/Origin,
and requires a per-session token for API access. It is not served through app
proxy routes. It is intended for the local user, not remote access or hosting.
