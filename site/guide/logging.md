# Logging

Logs tell you what the app is doing. Preá writes structured logs, as text on your machine and as JSON in production, with one line per request and the real cause of every error.

::: info What you'll learn
- How to write a log line
- The five levels and how to pick the minimum
- How the colors work
- What the request log shows
:::

## Write a log line

```go
logger.Debug("cache miss", "key", key)
logger.Info("sending email", "to", user.Email)
logger.Success("task created", "id", task.ID)
logger.Warn("retrying the payment", "attempt", n)
logger.Error("payment failed", "error", err)
```

After the message come key/value pairs. They become fields of the line, which keeps logs easy to search:

```
time=2026-10-15T12:49:30.137-03:00 level=SUCCESS msg="task created" id=1825b008-53da-4a61-b66a-1907998fd486
```

## Levels

From the most to the least detailed: **debug < info < success < warn < error**. The configured level is the minimum that shows up.

```yaml
logging:
  level: info     # debug | info | success | warn | error
  format: text    # text | json
```

A new project uses `debug` in `dev`, `info` in the base file, `json` in `prod` and `error` in `tests`. At `debug`, the routes that were mounted show up too.

The settings can also come from code, in `preas.Logging`, which takes precedence over the configuration:

```go
var Logging = prea.Logging{Level: "debug"}
```

## Colors

In a terminal, text logs show the level in color: **debug** in gray, **info** in green, **success** in bold green, **warn** in yellow and **error** in red. The time is dimmed.

Colors turn off on their own when the output isn't a terminal, like a file, a pipe or most CI runners, and in the `json` format. To turn them off in a terminal, set `NO_COLOR=1`.

## The request log

`core.RequestLogger`, in a new project's middlewares, writes one line per request:

```
level=SUCCESS msg=request method=POST path=/task status=201 latency=18.6ms ip=::1
level=INFO msg=request method=GET path=/task/42 status=404 latency=1.2ms ip=::1 error="Error #01: not found\n"
```

The level follows the status: **success** below 400, **info** for 4xx and **error** for 5xx. When a handler calls `core.Error`, the full error goes in the line, while the client only sees its short message.

`core.Recovery` turns a panic into a 500 reply and logs it with the stack trace, so one broken request never takes the app down.

## Other libraries

Preá's logger is also the default of Go's `log/slog`. Libraries that log with `slog` or `log` end up in the same output, with the same format.

`logger.Log` is the `*slog.Logger` behind the functions. For a logger of your own, like one that writes to a buffer in a test, use `logger.New(w, level, json)`.

## Next steps

- [Testing](./testing)
- [Deploying](./deploying)
