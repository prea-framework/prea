# Go API

Preá is a set of Go modules. A project created by `prea new` depends on the root module, which brings the others. The complete API of each one, with every type and function, is on pkg.go.dev.

| Module | What it has |
|---|---|
| [`github.com/prea-framework/prea`](https://pkg.go.dev/github.com/prea-framework/prea) | `Run` and `Test`, `Config`, `Logging` |
| [`…/core`](https://pkg.go.dev/github.com/prea-framework/prea/core) | `Module`, `App`, `Router`, `Middleware`, `Security`, `CORS`, `CSRF`, `Server`, `Reply`, `Error`, `NewError`, `RequestLogger`, `Recovery` |
| [`…/di`](https://pkg.go.dev/github.com/prea-framework/prea/di) | the container: `Export`, `Named`, `MissingError` |
| [`…/database`](https://pkg.go.dev/github.com/prea-framework/prea/database) | `BaseRepository`, `Prepare`, `Statement`, `Args`, `Transactional`, the errors, `Setup` and the migrations |
| [`…/config`](https://pkg.go.dev/github.com/prea-framework/prea/config) | `Load`, `Config.Value`, `Config.Profile` |
| [`…/security`](https://pkg.go.dev/github.com/prea-framework/prea/security) | `NewJwt`, `CheckSignKey`, `HashPassword`, `ComparePassword`, `ErrInvalidToken`, `ErrPasswordTooLong` |
| [`…/logger`](https://pkg.go.dev/github.com/prea-framework/prea/logger) | `Debug`, `Info`, `Success`, `Error`, `Configure`, `New` |
| [`…/cmd/prea`](https://pkg.go.dev/github.com/prea-framework/prea/cmd/prea) | the CLI |

## Using a module on its own

Each module has its own version and its own dependencies. An app that only needs HTTP and components can depend on `core` alone, without the database driver or the token library:

```bash
go get github.com/prea-framework/prea/core
```

It then builds a `core.App` itself, instead of calling `prea.Run`:

```go
app := core.App{
	Modules:  []*core.Module{task.Module},
	Security: core.Security{Headers: core.DefaultHeaders},
}
if err := app.Run(":8000"); err != nil {
	log.Fatal(err)
}
```

## Versions

Each module is tagged on its own, with its folder as a prefix: `core/v0.0.2`, `cmd/prea/v0.0.2`. The root module's tags have no prefix: `v0.0.2`. Every module shares the same major version.
