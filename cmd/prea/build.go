package main

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
)

func buildCommand() *cli.Command {
	return &cli.Command{
		Name:            "build",
		Usage:           "Build the project into bin/<name>; go build flags pass through (-o, -trimpath...)",
		ArgsUsage:       "[go build flags]",
		SkipFlagParsing: true,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if !slices.ContainsFunc(args, func(a string) bool { return a == "-o" || strings.HasPrefix(a, "-o=") }) {
				project, err := projectModule()
				if err != nil {
					return err
				}
				args = append([]string{"-o", exe(filepath.Join("bin", path.Base(project)))}, args...)
			}
			return goRun(ctx, nil, append(append([]string{"build"}, args...), ".")...)
		},
	}
}

// testsCommand runs go test with the tests profile (config/application-tests.yaml).
func testsCommand() *cli.Command {
	return &cli.Command{
		Name:            "tests",
		Usage:           "Run the tests with the tests profile, one package at a time; go test flags and packages pass through (default ./...)",
		ArgsUsage:       "[go test flags] [packages]",
		SkipFlagParsing: true,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			// Packages are paths (./preas/...); flag values like -run TestX are not.
			if !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, ".") }) {
				args = append(args, "./...")
			}
			// One package at a time: they share the tests database, and two
			// packages creating the same tables at once (schema: create-drop)
			// collide. An explicit -p wins.
			// ponytail: serializes every package; a database per test (step 11)
			// lets them run in parallel again.
			if !slices.ContainsFunc(args, func(a string) bool { return a == "-p" || strings.HasPrefix(a, "-p=") }) {
				args = append([]string{"-p", "1"}, args...)
			}
			return goRun(ctx, []string{"PREA_PROFILE=tests"}, append([]string{"test"}, args...)...) // config.ProfileEnv
		},
	}
}

// goRun runs the go command in the current directory with the extra env.
func goRun(ctx context.Context, env []string, args ...string) error {
	c := exec.CommandContext(ctx, "go", args...)
	c.Env = append(os.Environ(), env...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
