# CLI

Every command of `prea`, with its flags. Run the commands from the project root: the CLI reads `go.mod` to learn the project's import path.

```bash
prea --help
prea <command> --help
```

## `prea new`

Creates a project in a new folder.

```bash
prea new <name> [--module path] [--with-db] [--local clone]
```

| Flag | What it does |
|---|---|
| `--module` | the Go module path, used in `go.mod` and in the imports. Default: the name. Example: `github.com/you/shop` |
| `--with-db` | configures a database in the profiles and generates a preá with a table and full CRUD (`item`) |
| `--local` | uses the framework from a local clone instead of downloading it, to develop the framework itself |

The folder must not exist or must be empty; a folder with files is refused and left untouched. If anything fails, the new folder is removed.

## `prea dev`

Builds and runs the project, and restarts it when a `.go` file (tests excluded), `config/` or `.env` changes.

```bash
prea dev [--profile name]
```

| Flag | Default |
|---|---|
| `--profile` | `dev` |

- After a change, it waits until the files stop changing, so a save that writes in steps rebuilds once.
- If the new code doesn't compile, the error is shown and the running version keeps serving.
- Ctrl+C stops the app gracefully.

`prea run` and `prea dev` forward the stop signals to the app, which runs in its own process group. The app gets the signal however it was sent: Ctrl+C, an IDE's stop button, `kill` or a closing terminal. A second signal stops it at once.

## `prea run`

Builds and runs the project once, passing the stop signals to the app for a graceful shutdown.

```bash
prea run [--profile name]
```

Without `--profile`, the profile comes from `PREA_PROFILE`, in the environment or `.env`.

## `prea build`

Builds the binary.

```bash
prea build [go build flags]
```

- Without `-o`, the binary goes to `bin/<last element of the module path>`, with `.exe` added on Windows.
- Every flag goes to `go build`: `-o`, `-trimpath`, `-ldflags`...
- `GOOS` and `GOARCH` come from the environment.

## `prea tests`

Runs the tests with the `tests` profile.

```bash
prea tests [go test flags] [packages]
```

- Every flag goes to `go test`: `-run`, `-v`, `-count`...
- Without a package path (one starting with `.`), it tests `./...`.
- It runs one package at a time (`-p 1`), because the packages share the test database. An explicit `-p` wins.
- It sets `PREA_PROFILE=tests`, overriding the environment.

## `prea generate`

Short forms: `prea gen`, `prea g`.

### `prea g p <name>`

Generates a preá (`prea`): `preas/<name>/` with a module, a handler, a service, a repository, a model and two request bodies, and adds it to `Modules` in `preas/preas.go`.

### `prea g m <name> [--func]`

Generates a middleware (`middleware`) in `middleware/<name>.go`: a component, or a plain function with `--func`.

### Names

Write names with letters only, in camel case or with underscores. `OrderItem` and `order_item` are the same name. Each part of the code gets the form its convention asks for:

| `prea g p order_item` | |
|---|---|
| folder and package | `orderitem` |
| types | `OrderItem` |
| table | `order_item` |
| routes | `/order-item` |
| middleware file (`prea g m rate_limit`) | `middleware/rate_limit.go`, type `RateLimit` |

The CLI checks everything before writing. If a check fails, nothing changes:

- the name has only letters and isn't a Go keyword (`type`);
- the package doesn't clash with an import of `preas/preas.go` (`core`, `time`);
- the preá folder or the middleware file doesn't exist yet;
- the middleware's type isn't declared in the package already;
- `preas/preas.go` has a parenthesized import block and `var Modules = []*core.Module{...}`.

## `prea migrate`

Manages the database migrations. The folder is `database.migrations.dir` (default `migrations`).

```bash
prea migrate [--profile name] <command>
```

| Command | What it does |
|---|---|
| `create <name>` | creates `<next version>_<name>.sql`, empty, to write by hand. The name is snake_case |
| `up [version]` | applies the pending migrations, or those up to `version` |
| `down [n]` | undoes the last `n` applied migrations (default 1). Refuses one without `-- prea:down` |
| `status` | lists each migration: `applied` with its date, or `pending` |
| `validate` | checks the files without running them |

`--profile` goes before the command: `prea migrate --profile prod status`.

The CLI builds the app and runs it with `PREA_MIGRATE` set. Instead of serving, the app runs the command with its own configuration and database. This happens before the token secret is checked, so migrations don't need it.
