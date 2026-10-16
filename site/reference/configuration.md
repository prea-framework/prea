# Configuration keys

Every key Preá reads from `config/application.yaml` and the profile files. Your own keys can sit next to these and are read with `value:"key"`.

## Environment

| Variable | What it does |
|---|---|
| `PREA_PROFILE` | the active profile: loads `config/application-<profile>.yaml` on top of the base |
| any `${NAME}` | replaced by the variable's value where the YAML references it |

`.env` in the working directory is read first and never overrides a variable that is already set. A value that is only `${NAME}`, with `NAME` unset, counts as missing.

## Server

| Key | Required | Default | What it does |
|---|---|---|---|
| `server.port` | **yes** | — | the port to listen on |
| `server.mode` | **yes** | — | `debug`, `release` or `test`. `debug` indents the JSON and logs the routes |
| `server.read_header_timeout` | no | `5s` | time to read the request headers; protects against clients that send them very slowly |
| `server.read_timeout` | no | `30s` | time to read the whole request, body included |
| `server.write_timeout` | no | `30s` | time to write the reply; raise it for streaming and large downloads |
| `server.idle_timeout` | no | `120s` | how long an idle keep-alive connection stays open |
| `server.shutdown_timeout` | no | `15s` | how long the requests in flight get to finish on shutdown |
| `server.max_header_bytes` | no | `1048576` | the largest request headers, in bytes (1 MB) |
| `server.trusted_proxies` | no | none | proxies whose `X-Forwarded-For` is trusted. `Security.TrustedProxies` in code wins |

Durations use Go's format: `500ms`, `30s`, `2m`.

## Logging

| Key | Default | Values |
|---|---|---|
| `logging.level` | `info` | `debug`, `info`, `success`, `error` |
| `logging.format` | `text` | `text`, `json` |

`preas.Logging` in code wins over both.

## Database

| Key | Default | What it does |
|---|---|---|
| `database.url` | none | the PostgreSQL connection string. Without it, the app has no database |
| `database.schema` | `none` | `none`, `validate`, `update`, `create` or `create-drop`: what to do with the tables at startup ([Models & schema](/guide/models#the-schema-setting)) |
| `database.migrations.dir` | `migrations` | the folder of the migration files |
| `database.migrations.version` | `semver` | `semver` (`0.0.1`) or `timestamp` (`20261013120000`) |
| `database.migrations.on_start` | `false` | applies the pending migrations at startup |
| `database.migrations.from_models` | `false` | writes a migration with the difference between the models and the database at startup |

The connection pool is sized in the URL: `?pool_max_conns=20`. The default is 4 or the number of CPUs, whichever is larger.

## Security

| Key | Default | What it does |
|---|---|---|
| `security.jwt.secret` | none | checked at startup when set (at least 32 bytes). Pass the same value to `security.NewJwt` when the app signs tokens |}

## In code

These live in `preas/preas.go`, not in the configuration:

| Variable | What it holds |
|---|---|
| `Modules` | the preás of the app |
| `Middlewares` | the middlewares of every request |
| `Security` | CORS, CSRF, headers, trusted proxies and your own protections |
| `Logging` | log level and format, overriding the configuration |
