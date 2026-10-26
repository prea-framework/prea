# Changelog

What each version brought. Preá is pre-alpha, and the API may change between versions until `v1`.

Each module has its own version. The CLI and the root module follow the newest one.

## v0.0.4

*October 2026*

Modules: `security` v0.0.3, `database` v0.0.3, `logger` v0.0.2; the root module and the CLI v0.0.4.

**Security**
- Each `security.Jwt` has its own signing key. Build one with `security.NewJwt[T](secret)`; the process-wide `SignKey` and `SetSignKey` are gone, so two apps in the same process never share or overwrite a secret. `prea.Run` still checks `security.jwt.secret` at startup when it is set. *Breaking:* replace `var tokens security.Jwt[T]` with `NewJwt`.
- `HashPassword` and `ComparePassword` reject passwords longer than 128 bytes with `security.ErrPasswordTooLong`, before argon2 runs, so a huge body can't burn CPU and memory.

**Logs**
- Text logs are colored in a terminal: debug in gray, info in green, success in bold green, warn in yellow and error in red, with the time dimmed. Files, pipes, the `json` format and `NO_COLOR=1` stay plain.
- A new `warn` level, between success and error, with `logger.Warn`.

**Database**
- A migration written from the models is named after what it does, like `0.0.1_create_task_table.sql`, `0.0.2_alter_task_table.sql` or `0.0.2_create_list_table_and_alter_task_table.sql`, instead of `<version>_models.sql`. Migrations already written keep their names.

**Docs**
- The tutorial says which settings write the migrations from the models, and what to do when none appears.

**Thanks** to [@Carlinnn](https://github.com/Carlinnn) and [@MikaelDiogo](https://github.com/MikaelDiogo) for their first contributions.

## v0.0.3

*October 2026*

**HTTP**
- `GET` routes also answer `HEAD`, with the same headers and no body.
- A shutdown that runs past `server.shutdown_timeout` returns `core.ErrShutdownTimeout`, with a message that says what happened.

**Database**
- A foreign key column takes the type of the key it references: `ListID *string` with `fk:"list"` is a `uuid` column, with no `type` tag.
- `Create` leaves nil pointers out, so the column default applies.
- A value too long for its column, out of range or an invalid date is a 400, like other bad input.
- An invalid model reports only its own errors, without the foreign key errors that followed from it.

**CLI**
- `prea tests` runs one package at a time: the packages share the test database.
- `prea run` and `prea dev` forward every stop signal to the app, so an IDE's stop button, `kill` or a closing terminal stop it gracefully.
- Generated names follow Go initialisms (`APIKey`), and the generated code is shorter, with grouped imports.

**Docs**
- This site, the tutorial and runnable examples on pkg.go.dev.
- Preá is now under the MIT License.

## v0.0.2

*October 2026*

**Run an app from its configuration**
- `prea.Run` starts an app from `config/` and the active profile: logs, database, migrations, token secret and server.
- `prea.Test` builds the app for a test, with the `tests` profile.
- The server timeouts come from `server.*` in the configuration.
- The token secret is optional: an app that doesn't use tokens doesn't need it.

**CLI**
- `prea new` creates a project, with or without a database (`--with-db`).
- `prea dev` runs the app and restarts it on every change.
- `prea build` builds the binary into `bin/`.
- `prea tests` runs the tests with the `tests` profile.

**Components**
- A missing dependency is a typed error, `*di.MissingError`, and a repository without a database says to set `database.url`.

## v0.0.1

*October 2026*

The first version.

- **Preás and components**, with `inject:""`, `value:""`, named components, exports and imports, and `Init`/`Close`.
- **Configuration** with profiles, `.env` and environment variables.
- **HTTP**: routes, middlewares at three levels, the standard reply, errors with a status, CORS, CSRF, security headers and graceful shutdown.
- **Database** for PostgreSQL: models described by tags, repositories, named-parameter queries, transactions, schema checks and migrations written from the models.
- **Security**: signed tokens and password hashing.
- **Logs**: four levels, text or JSON, one line per request.
- **CLI**: `prea run`, `prea generate` and `prea migrate`.
- **Modules**: the framework is split into Go modules, each with its own version.
