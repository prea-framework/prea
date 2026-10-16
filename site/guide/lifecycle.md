# App lifecycle

This page follows an app from `prea.Run` to the moment it stops. Knowing the order helps you understand when your code runs, and why most mistakes stop the app before it serves.

::: info What you'll learn
- Everything `prea.Run` does at startup, in order
- When the components' `Init` and `Close` run
- How the app shuts down without dropping requests
:::

## Startup

`prea.Run` goes through these steps. If any of them fails, it logs the error and the process exits with code 1.

1. **Load the configuration.** It reads `.env`, then `config/application.yaml` and the active profile's file. `server.port` and `server.mode` must be set.
2. **Set up the logs.** It applies `logging.level` and `logging.format`, or `preas.Logging` when set.
3. **Connect to the database**, if `database.url` is set. It checks the connection right away, so a wrong address fails now and not on the first request.
4. **Run a migration command and exit**, when started by `prea migrate`. [Migrations](./migrations#the-cli) explains this.
5. **Check the token secret**, if `security.jwt.secret` is set. A secret shorter than 32 bytes stops the app. The app still builds its own `security.Jwt` with `NewJwt` when it needs to sign tokens.
6. **Create the components.** It loads every preá in `Modules`, creates their components and fills their `inject:""` and `value:""` fields. A missing dependency stops the app here.
7. **Prepare the database.** It generates and applies migrations and checks the tables against the models, depending on the [database settings](./migrations#at-startup).
8. **Start the components.** It calls each `Init`, dependencies first.
9. **Mount the routes** of every handler, with the security protections and the middlewares.
10. **Serve** on `server.port`.

```
level=INFO msg="prea: config loaded" profile=dev
level=INFO msg="database: applied 0.0.2_add_due_date (4ms)"
level=INFO msg="prea: listening" addr=[::]:8000
```

## While serving

Each request runs in its own goroutine. The components are shared by all of them, which is why a component [never keeps request data in its fields](./dependency-injection#one-instance-many-requests).

The [server settings](/reference/configuration#server) protect the app from slow or stuck clients: how long a client may take to send the headers, the whole request, or to read the reply.

## Shutdown

When the process gets `SIGINT` (Ctrl+C) or `SIGTERM` (what deploy platforms send):

1. The server **stops accepting** new connections.
2. It **waits** for the requests in flight, up to `server.shutdown_timeout` (15 seconds by default).
3. It calls each component's **`Close`**, in the reverse order of `Init`.
4. It **closes the database**, after the components, so no query loses its connection halfway.

```
level=INFO msg="prea: shutting down"
level=SUCCESS msg="prea: stopped"
```

If the requests are still running when the timeout ends, their connections close and their context is cancelled. That is one more reason to pass `c.Request.Context()` to every query. The app then exits with an error:

```
level=ERROR msg="prea: stopped" error="prea: shutdown timed out: the requests still running were cancelled (server.shutdown_timeout)"
```

A second Ctrl+C stops the process at once, without waiting.

## Next steps

- [Models & schema](./models): the database, starting from your structs
- [Deploying](./deploying): the lifecycle in production
