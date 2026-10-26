# 2. Your first preá

A **preá** is one feature of the app, in its own folder. In this chapter you generate the `task` preá and see what each of its files does.

## Generate it

With `prea dev` still running, in another terminal:

```bash
prea g p task
```

```
preá "task" created at preas/task and registered in preas/preas.go
```

`g p` is short for `generate prea`. Look at `preas/preas.go`: the CLI added `task.Module` to the list for you.

`prea dev` notices the new files and restarts:

```
level=SUCCESS msg="database: generated a migration from the models: review it before committing" file=migrations/0.0.1_create_task_table.sql
level=INFO msg="database: applied 0.0.1_create_task_table (3ms)"
level=INFO msg="prea: listening" addr=[::]:8000
```

The new preá came with a database table, and Preá created it for you. More on that in the next chapter.

## Try it

```bash
curl -X POST localhost:8000/task -H 'Content-Type: application/json' -d '{"name": "Buy coffee"}'
```

```json
{
    "code": 1,
    "data": {
        "id": "a6c20939-c361-4649-9a2a-c9a1026b7cb1",
        "name": "Buy coffee"
    }
}
```

```bash
curl localhost:8000/task
```

The task is in the list. The preá already answers five routes:

| Route | Does |
|---|---|
| `GET /task` | lists every task |
| `GET /task/:id` | returns one task |
| `POST /task` | creates a task |
| `PATCH /task/:id` | changes a task |
| `DELETE /task/:id` | deletes a task |

## The files

```
preas/task/
├── module.go        what the preá is made of
├── handler.go       the routes
├── service.go       the rules
├── repository.go    the database access
├── model.go         the table
└── dtos/            the request bodies
```

Each file has one job. A request goes down through them and the reply comes back up:

```
request → handler → service → repository → database
```

**`module.go`** declares the preá's **components**, the structs Preá creates for it:

```go
var Module = &core.Module{
	Name: "task",
	Providers: []any{
		Repository{},
		di.Export(Service{}),
		Handler{},
	},
}
```

**`handler.go`** declares the routes, reads the request and writes the reply:

```go
type Handler struct {
	Service *Service `inject:""`
}
```

Notice the `inject:""` tag. The handler doesn't create its service: it **asks** for one, and Preá fills the field when the app starts. The service asks for the repository the same way. This is how the pieces of an app find each other, and it is the heart of Preá. [Components & injection](/guide/dependency-injection) tells the whole story.

**`service.go`** is where the rules go. It is almost empty for now: it passes everything to the repository.

**`repository.go`** is a single line that gives the preá a complete set of database operations:

```go
type Repository struct {
	database.BaseRepository[Task]
}
```

## What you learned

- `prea g p` generates a preá with working routes and registers it.
- A preá splits its work into a handler, a service and a repository.
- A component asks for what it needs with `inject:""`, and Preá fills it in.

[Next: the model →](./the-model)
