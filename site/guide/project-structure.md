# Project structure

Every Preá project has the same layout. Once you know it, you can find your way around any Preá app.

::: info What you'll learn
- What each folder and file of a new project is for
- Where your own code goes
- How `main.go` starts the app
:::

## The layout

This is a project created by `prea new shop --with-db`:

```
shop/
├── go.mod
├── main.go                    starts the app
├── preas/
│   ├── preas.go               what the app is made of
│   ├── health/                GET /health
│   │   ├── module.go
│   │   ├── handler.go
│   │   └── health_test.go
│   └── item/                  an example feature, with a database table
│       ├── module.go
│       ├── handler.go
│       ├── service.go
│       ├── repository.go
│       ├── model.go
│       └── dtos/
│           ├── create_request.go
│           └── update_request.go
├── config/
│   ├── application.yaml       base settings
│   ├── application-dev.yaml   settings for your machine
│   ├── application-prod.yaml  settings for production
│   └── application-tests.yaml settings for the tests
├── migrations/                SQL files that change the database
├── middleware/                your middlewares
├── tests/
│   └── app_test.go            tests of the whole app
├── .env.example               a template for your local .env
└── .gitignore
```

## `main.go`

`main.go` is short on purpose. It hands what your app declares to `prea.Run`:

```go
package main

import (
	"github.com/prea-framework/prea"

	"shop/preas"
)

func main() {
	prea.Run(prea.Config{
		Modules:     preas.Modules,
		Middlewares: preas.Middlewares,
		Security:    preas.Security,
		Logging:     preas.Logging,
	})
}
```

You rarely touch it. `prea.Run` loads the configuration, connects to the database, builds the preás and serves until you stop it. [App lifecycle](./lifecycle) lists every step.

## `preas/preas.go`

This file declares what the app is made of:

```go
// The features of the app. prea g p adds new preás here.
var Modules = []*core.Module{
	health.Module,
	item.Module,
}

// Middlewares run on every request.
var Middlewares = []any{
	core.RequestLogger,
	core.Recovery,
}

// Security runs before everything else.
var Security = core.Security{
	CSRF:    &core.CSRF{},
	Headers: core.DefaultHeaders,
}

// Logging left empty takes its settings from the configuration.
var Logging = prea.Logging{}
```

A preá that isn't listed in `Modules` is not part of the app. When you generate one with `prea g p`, the CLI adds it to the list for you.

## A preá's folder

Each folder in `preas/` is one feature. The files split the work into layers, and each layer has one job:

| File | Its job |
|---|---|
| `module.go` | declares the preá: its components, what it imports and what it shares |
| `handler.go` | HTTP only: routes, reading the request, writing the reply |
| `service.go` | the rules of the feature: what is allowed, what happens |
| `repository.go` | reading and writing the database |
| `model.go` | the shape of a row in the table |
| `dtos/` | the shape of the request bodies, one file per body |

A preá doesn't need every file. `health` has no database, so it has only a module and a handler.

## `config/`

The settings live in YAML files. `application.yaml` is always loaded; the file of the active **profile** is loaded on top of it. The profile comes from `PREA_PROFILE`, which `.env` sets to `dev` on your machine. [Configuration](./configuration) explains the details.

## `migrations/`

Each file is a change to the database, in plain SQL, named by version: `0.0.1_create_task_table.sql`, `0.0.2_add_due_date.sql`. In the `dev` profile, Preá writes these files for you when a model changes. [Migrations](./migrations) has the full story.

## `tests/`

The tests of each preá live next to its code, like `preas/health/health_test.go`. The `tests/` folder is for tests of the whole app, with every preá and middleware. `prea tests` runs both. [Testing](./testing) shows how to write them.

## Naming

Preá follows the Go conventions and derives every name from the one you give the CLI:

| `prea g p order_item` | |
|---|---|
| folder and package | `preas/orderitem`, package `orderitem` |
| types | `OrderItem` |
| table | `order_item` |
| routes | `/order-item` |

## Next steps

- [Learn what a preá is](./preas)
- [Learn how components get what they need](./dependency-injection)
