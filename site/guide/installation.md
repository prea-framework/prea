# Installation

You need Go, the `prea` CLI and, if your app stores data, PostgreSQL.

::: info What you'll learn
- What to install before you start
- Two ways to install the CLI
- How to check that everything works
:::

## Requirements

| What | Version | Why |
|---|---|---|
| [Go](https://go.dev/dl/) | 1.26 or newer | Preá is a Go framework |
| [PostgreSQL](https://www.postgresql.org/download/) | 13 or newer | only if your app uses a database |

::: tip No PostgreSQL on your machine?
Run it in a container. This starts one, with a database for the app and another for its tests:

```bash
docker run -d --name postgres -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:17-alpine
docker exec postgres createdb -U postgres todo
docker exec postgres createdb -U postgres todo_test
```
:::

## Install the CLI

::: code-group

```bash [Install script]
curl -fsSL https://raw.githubusercontent.com/prea-framework/prea/main/install.sh | sh
```

```bash [go install]
go install github.com/prea-framework/prea/cmd/prea@latest
```

:::

The install script runs `go install` for you and adds Go's `bin` folder to your `PATH` in your shell's startup file if it isn't there yet. It works with zsh, bash, fish and sh.

To install a specific version, set `PREA_VERSION`:

```bash
curl -fsSL https://raw.githubusercontent.com/prea-framework/prea/main/install.sh | PREA_VERSION=v0.0.4 sh
```

## Check the installation

```bash
prea --help
```

You should see the list of commands:

```
COMMANDS:
   new               Create a project: main.go, preas/, config/ with the profiles, tests/ and a health preá
   run               Build and run the project (the main package at the root)
   dev               Run the project and restart it when a .go file, config/ or .env changes (profile dev by default)
   build             Build the project into bin/<name>; go build flags pass through (-o, -trimpath...)
   tests             Run the tests with the tests profile, one package at a time; go test flags and packages pass through (default ./...)
   generate, gen, g  Generate framework code
   migrate           Manage the database migrations (database.migrations in the config)
```

::: warning `prea: command not found`?
Open a new terminal so the shell reads the updated `PATH`. If it still fails, add Go's `bin` folder yourself: `export PATH="$PATH:$(go env GOPATH)/bin"`.
:::

## The framework itself

You don't install the framework separately. Every project created with `prea new` lists it in its `go.mod`, and Go downloads it the first time you build.

## Next steps

- [Create and run your first app](./quick-start)
