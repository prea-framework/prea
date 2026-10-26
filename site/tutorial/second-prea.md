# 5. A second preá

Tasks are piling up. In this chapter you add **lists**, like "Groceries" and "Work", and let a task belong to one. You learn how one preá uses another, and how two tables relate.

## Generate the list preá

```bash
prea g p list
```

Give a list a name, short and unique. In `preas/list/model.go`:

<<< @/examples/todo/preas/list/model.go#model

`unique:""` means no two lists can share a name. The rest of the generated `list` preá already does what it needs to.

## Point a task at a list

A task may belong to a list, so the reference is a pointer: `nil` means "no list". Add `ListID` to `preas/task/model.go`:

<<< @/examples/todo/preas/task/model.go#model

- `fk:"list"` makes it a reference to the `list` table: a task can't point to a list that doesn't exist.
- `ondelete:"cascade"` deletes a list's tasks when the list is deleted.
- `index:""` makes "the tasks of this list" a fast query.
- The column takes the type of the list's key, `uuid`, on its own.

Let clients send it when they create a task, in `preas/task/dtos/create_request.go`:

<<< @/examples/todo/preas/task/dtos/create_request.go#create

## Use one preá from another

Before creating a task, the task service should check that the list exists, and that check belongs to the list preá. A preá can only use what another preá **exports**, and only after it **imports** it.

The list preá already exports its service. Look at `preas/list/module.go`:

```go
Providers: []any{
	Repository{},
	di.Export(Service{}), // other preás may use it
	Handler{},
},
```

Make `task` import `list`, in `preas/task/module.go`:

<<< @/examples/todo/preas/task/module.go#module

Now the task service can ask for the list service, like it asks for its repository:

<<< @/examples/todo/preas/task/service.go#service

## The new rules

Check the list when one is given:

<<< @/examples/todo/preas/task/service.go#create

The check itself turns a missing list into a clear error. Add it next to `checkTitle`:

```go
// checkList turns a missing list into a clear 422, instead of the
// database's foreign key error.
func (s *Service) checkList(ctx context.Context, id string) error {
	_, err := s.Lists.Get(ctx, id)
	if errors.Is(err, database.ErrNotFound) {
		return core.NewError(http.StatusUnprocessableEntity, "list not found")
	}
	return err
}
```

The service file now uses three more imports: `"errors"`, `"github.com/prea-framework/prea/database"` and `"todo/preas/list"`.

Finally, let `GET /task?list=<id>` return only one list's tasks. In the service:

<<< @/examples/todo/preas/task/service.go#list

And in the handler, pass the query parameter along:

```go
func (h *Handler) list(c *gin.Context) {
	items, err := h.Service.List(c.Request.Context()) // [!code --]
	items, err := h.Service.List(c.Request.Context(), c.Query("list")) // [!code ++]
```

## Try it

`prea dev` restarts and writes `migrations/0.0.2_create_list_table_and_alter_task_table.sql`, with the new table, the new column and the reference:

```sql
create table if not exists "list" (
	"id" uuid not null default gen_random_uuid(),
	"name" varchar(80) not null,
	primary key ("id")
);

create unique index if not exists "list_name_key" on "list" ("name");

alter table "task" add column "list_id" uuid references "list" on delete cascade;

create index if not exists "task_list_id_idx" on "task" ("list_id");
```

```bash
curl -X POST localhost:8000/list -H 'Content-Type: application/json' -d '{"name": "Groceries"}'
# → {"code": 1, "data": {"id": "e08e78a2-…", "name": "Groceries"}}

curl -X POST localhost:8000/task -H 'Content-Type: application/json' \
  -d '{"title": "Buy bread", "listId": "e08e78a2-5b74-41f4-9b90-57395f0760c2"}'

curl 'localhost:8000/task?list=e08e78a2-5b74-41f4-9b90-57395f0760c2'
# → only "Buy bread"
```

And the rules at work:

```bash
curl -X POST localhost:8000/task -H 'Content-Type: application/json' \
  -d '{"title": "Call mom", "listId": "6d0b1f5e-3c2a-4b7e-9f10-2a3b4c5d6e7f"}'
# → 422 {"text": "list not found", "code": 2}

curl -X POST localhost:8000/list -H 'Content-Type: application/json' -d '{"name": "Groceries"}'
# → 409 {"text": "conflict", "code": 2}
```

The 409 came from the `unique` name, without a line of code from you.

## What you learned

- A preá uses another by importing it, and only sees what that preá exports.
- `fk`, `ondelete` and `index` describe a relation between tables.
- Database errors, like a duplicate unique value, become the right HTTP status on their own.

[Next: a middleware →](./middleware)
