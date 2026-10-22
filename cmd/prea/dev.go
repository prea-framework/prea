package main

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

func devCommand() *cli.Command {
	return &cli.Command{
		Name:  "dev",
		Usage: "Run the project and restart it when a .go file, config/ or .env changes (profile dev by default)",
		Flags: []cli.Flag{profileFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return dev(ctx, cmp.Or(cmd.String("profile"), "dev"), 500*time.Millisecond)
		},
	}
}

// dev builds and runs the project, then polls the sources every interval.
// After a change, once nothing changed for one more interval, it rebuilds: a
// good build replaces the running server (SIGTERM, so it shuts down
// gracefully), a failed one keeps it. It stops the server when ctx ends or on
// SIGINT/SIGTERM, which it forwards: the server runs in its own process group.
//
// ponytail: polling, not fsnotify (which isn't recursive); a tree walk per
// interval is cheap at an app's size.
func dev(ctx context.Context, profile string, interval time.Duration) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, stopSignals...)
	defer signal.Stop(signals)
	dir, err := os.MkdirTemp("", "prea-dev")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	var server *exec.Cmd
	defer func() {
		if server != nil && server.Process != nil {
			// While it shuts down, a second Ctrl+C stops it at once.
			go forward(signals, server)
		}
		halt(server)
	}()
	sleep := func() bool { // false: stop
		select {
		case <-ctx.Done():
			return false
		case <-signals:
			return false
		case <-time.After(interval):
			return true
		}
	}

	seen := sources()
	for n := 0; ; n++ {
		bin := exe(filepath.Join(dir, fmt.Sprint("server-", n)))
		if err := buildServer(ctx, bin); err != nil {
			fmt.Fprintln(os.Stderr, "prea dev:", err, "(the running version keeps serving)")
		} else {
			halt(server)
			server = exec.Command(bin)
			server.Env = append(os.Environ(), "PREA_PROFILE="+profile) // config.ProfileEnv
			server.Stdout, server.Stderr = os.Stdout, os.Stderr
			ownGroup(server)
			if err := server.Start(); err != nil {
				return err
			}
		}

		// Wait for a change, then for the sources to settle (editors save in steps).
		for changed := false; !changed; {
			if !sleep() {
				return nil
			}
			now := sources()
			changed = !maps.Equal(now, seen)
			seen = now
		}
		for settled := false; !settled; {
			if !sleep() {
				return nil
			}
			now := sources()
			settled = maps.Equal(now, seen)
			seen = now
		}
		fmt.Fprintln(os.Stderr, "prea dev: change detected, rebuilding")
	}
}

// halt stops server gracefully and waits for it; nil or exited is fine.
func halt(server *exec.Cmd) {
	if server == nil || server.Process == nil {
		return
	}
	if err := server.Process.Signal(syscall.SIGTERM); err != nil {
		server.Process.Kill() // Windows cannot send SIGTERM: without this Wait never returns
	}
	server.Wait()
}

// sources maps what dev watches to its modification time: the .go files
// (tests don't restart the server), config/ and .env. It skips hidden
// directories and bin/.
func sources() map[string]time.Time {
	files := map[string]time.Time{}
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(name, ".") || name == "bin" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		watched := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") ||
			path == ".env" || strings.HasPrefix(path, "config"+string(filepath.Separator))
		if info, err := d.Info(); watched && err == nil {
			files[path] = info.ModTime()
		}
		return nil
	})
	return files
}
