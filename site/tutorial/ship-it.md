# 9. Ship it

The API is done and tested. In this last chapter you build the binary, prepare the database of production and run the app the way a server would.

## Build

```bash
prea build
```

The binary lands in `bin/todo`. It holds the whole app, framework included. To build for a Linux server from another system:

```bash
GOOS=linux GOARCH=amd64 prea build
```

## What travels with it

The binary reads two folders from the directory it runs in:

```
release/
├── todo           the binary
├── config/        the settings
└── migrations/    the database changes
```

Copy the three together to wherever the app will run.

## Prepare the production database

In production, nobody writes migrations: they are the reviewed files you committed. Make that explicit in `config/application-prod.yaml`:

```yaml
server:
  mode: release
logging:
  format: json
database:
  migrations:
    on_start: true # apply the committed migrations when the app starts
```

The base file already has `schema: validate`, so the app refuses to start if the tables don't match the models. `from_models` stays off, because only `dev` turns it on.

Check what production would apply:

```bash
prea migrate --profile prod status
```

```
0.0.1_create_task_table                  applied 2026-10-15 12:58:32
0.0.2_create_list_table_and_alter_task_table applied 2026-10-15 12:59:12
```

On a fresh production database, both would show `pending`, and `on_start` would apply them. If you prefer to migrate as a separate deploy step, leave `on_start` off and run `prea migrate --profile prod up` before starting the new version.

## Run it

On the server, the settings that are secret or specific to that machine come from the environment:

```bash
PREA_PROFILE=prod \
DATABASE_URL='postgres://…' \
TODO_API_KEY='a-long-random-production-key' \
./bin/todo
```

```json
{"level":"INFO","msg":"prea: config loaded","profile":"prod"}
{"level":"INFO","msg":"prea: listening","addr":"[::]:8000"}
```

When the server stops the app with `SIGTERM`, the app finishes the requests in flight before it exits:

```json
{"level":"INFO","msg":"prea: shutting down"}
{"level":"SUCCESS","msg":"prea: stopped"}
```

## You built it

In nine chapters you:

- created a project and two preás that use each other;
- described two related tables with structs, and let Preá write the migrations;
- wrote rules that answer with the right HTTP status;
- protected the API with a middleware that reads its settings and checks them at startup;
- set up profiles for your machine, the tests and production;
- tested a preá and the whole app;
- built and ran it like production.

The finished code is in [`site/examples/todo`](https://github.com/prea-framework/prea/tree/main/site/examples/todo).

## Where to go next

- [The guide](/guide/what-is-prea) goes deeper into every idea of this tutorial.
- [Transactions](/guide/transactions): several writes that succeed or fail together.
- [HTTP security](/guide/http-security): let a frontend call your API.
- [Deploying](/guide/deploying): containers and production checklists.
