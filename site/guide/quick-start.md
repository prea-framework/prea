# Quick start

In five minutes you will create a project, run it with hot reload and call its API.

::: info What you'll learn
- How to create a project with `prea new`
- How to run it with `prea dev`
- What a change looks like while the app runs
:::

Before you start, [install the CLI](./installation) and have PostgreSQL running.

## 1. Create a project

```bash
prea new shop --with-db
```

```
preá "item" created at preas/item and registered in preas/preas.go
project shop created: cd shop && cp .env.example .env && prea dev
```

`prea new` creates the folder `shop/` with a working app. `--with-db` connects it to a database and adds an example preá, `item`, with a full set of routes to create, list, update and delete items.

::: tip Without a database
`prea new shop` without the flag creates the same app with only a `GET /health` route and no database. You can add a database later by setting `database.url` in the configuration.
:::

## 2. Point it at your database

```bash
cd shop
cp .env.example .env
```

Open `.env` and set the two connection strings, one for the app and one for its tests:

```bash
PREA_PROFILE=dev
DATABASE_URL='postgres://postgres:postgres@localhost:5432/shop?sslmode=disable'
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/shop_test?sslmode=disable'
```

Both databases must exist. Create them with `createdb shop` and `createdb shop_test`, or with any PostgreSQL client.

## 3. Run it

```bash
prea dev
```

```
level=INFO msg="prea: config loaded" profile=dev
level=SUCCESS msg="database: generated a migration from the models: review it before committing" file=migrations/0.0.1_create_item_table.sql
level=INFO msg="database: applied 0.0.1_create_item_table (5ms)"
level=INFO msg="prea: listening" addr=[::]:8000
```

A lot just happened. Preá loaded the `dev` configuration, compared the `item` model with the empty database, wrote a migration to create its table and applied it. Then it started listening on port 8000.

## 4. Call the API

In another terminal:

```bash
curl -X POST localhost:8000/item -H 'Content-Type: application/json' -d '{"name": "Coffee beans"}'
```

```json
{
    "code": 1,
    "data": {
        "id": "1825b008-53da-4a61-b66a-1907998fd486",
        "name": "Coffee beans"
    }
}
```

```bash
curl localhost:8000/item           # list every item
curl localhost:8000/health         # {"code": 1, "data": {"status": "ok"}}
```

Every reply has the same shape: a `code` (1 for success), and `data` or a `text` message. [Errors & responses](./errors-and-responses) explains it.

## 5. Change something

Leave `prea dev` running. Open `preas/health/handler.go` and change the reply:

```go
core.Reply(c, http.StatusOK, core.Data(gin.H{"status": "ok"})) // [!code --]
core.Reply(c, http.StatusOK, core.Data(gin.H{"status": "great"})) // [!code ++]
```

Save the file. `prea dev` rebuilds and restarts the app, and `curl localhost:8000/health` now answers `"great"`. If you save code that doesn't compile, the error shows up in the terminal and the previous version keeps serving.

## 6. Run the tests

```bash
prea tests
```

The new project already has tests: one next to the `health` preá and one for the whole app in `tests/`. `prea tests` runs them with the `tests` profile, against the test database.

## What's in the box

```
shop/
├── main.go          starts the app
├── preas/           your features: health and item
├── config/          the settings, per profile
├── migrations/      the database changes, as SQL files
├── middleware/      your middlewares
└── tests/           tests of the whole app
```

[Project structure](./project-structure) walks through every file.

## Next steps

- [Build a todo API step by step](/tutorial/), the best way to learn Preá
- [Learn what a preá is](./preas)
