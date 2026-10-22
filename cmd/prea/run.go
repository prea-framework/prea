package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/urfave/cli/v3"
)

var profileFlag = &cli.StringFlag{
	Name:  "profile",
	Usage: "config profile: loads config/application-<profile>.yaml",
}

func runCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "Build and run the project (the main package at the root)",
		Flags: []cli.Flag{profileFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runServer(ctx, cmd)
		},
	}
}

// runServer builds the main package at the project root and runs it with
// the profile and env, until it exits. It runs the binary directly (not via
// go run) so it gets the signals and can shut down gracefully.
func runServer(ctx context.Context, cmd *cli.Command, env ...string) error {
	dir, err := os.MkdirTemp("", "prea-run")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	bin := exe(filepath.Join(dir, "server"))
	if err := buildServer(ctx, bin); err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, stopSignals...)
	defer signal.Stop(signals)

	server := exec.Command(bin)
	server.Env = append(os.Environ(), env...)
	if cmd.IsSet("profile") {
		server.Env = append(server.Env, "PREA_PROFILE="+cmd.String("profile")) // config.ProfileEnv
	}
	server.Stdout, server.Stderr = os.Stdout, os.Stderr
	ownGroup(server)
	if err := server.Start(); err != nil {
		return err
	}
	go forward(signals, server)
	// The server already logged why it stopped: exit with its code, without
	// adding "exit status 1".
	var exit *exec.ExitError
	if err := server.Wait(); errors.As(err, &exit) {
		return cli.Exit("", exit.ExitCode())
	} else if err != nil {
		return err
	}
	return nil
}

// stopSignals stop the server; the CLI forwards them, since the server runs
// in its own process group. SIGHUP is the terminal closing.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

// forward passes every signal to server: the first one shuts it down
// gracefully, a second one stops it at once. SIGHUP becomes SIGTERM, which
// the server shuts down on.
func forward(signals <-chan os.Signal, server *exec.Cmd) {
	for sig := range signals {
		if sig == syscall.SIGHUP {
			sig = syscall.SIGTERM
		}
		server.Process.Signal(sig)
	}
}

// exe adds the .exe suffix Windows needs to run a binary: go build -o does
// not add it.
func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// buildServer builds the main package at the project root into bin.
func buildServer(ctx context.Context, bin string) error {
	if err := goRun(ctx, nil, "build", "-o", bin, "."); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}
