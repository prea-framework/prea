// Package prea runs a Preá app: it loads the config of the active profile,
// opens the database, applies the migrations and serves the modules. The
// app's main only passes what it declares:
//
//	prea.Run(prea.Config{Modules: preas.Modules, Middlewares: preas.Middlewares, Security: preas.Security})
package prea

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prea-framework/prea/config"
	"github.com/prea-framework/prea/core"
	"github.com/prea-framework/prea/database"
	"github.com/prea-framework/prea/di"
	"github.com/prea-framework/prea/logger"
	"github.com/prea-framework/prea/security"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is what the app declares in code. The rest comes from
// config/application.yaml and the active profile:
//
//   - server.port and server.mode, required;
//   - server.read_header_timeout, read_timeout, write_timeout, idle_timeout,
//     shutdown_timeout, max_header_bytes and trusted_proxies, with defaults;
//   - database.url (without it the app has no database), database.schema
//     and database.migrations;
//   - security.jwt.secret, checked at startup only when set (apps build
//     security.Jwt with NewJwt using the same value).
type Config struct {
	Modules     []*core.Module
	Middlewares []any
	Security    core.Security // Security.TrustedProxies, when nil, comes from server.trusted_proxies
	Logging     Logging
}

// Logging configures the logs. An empty field comes from logging.level or
// logging.format in the config, then the default: info, text.
type Logging struct {
	Level  string `yaml:"level"`  // debug | info | success | error
	Format string `yaml:"format"` // text | json
}

const configDir = "config"

// Run starts the app and serves until SIGINT or SIGTERM; a startup error is
// logged and exits the process with code 1. With PREA_MIGRATE set (by prea
// migrate) it runs that migrations command instead of serving.
func Run(cfg Config) {
	if err := run(cfg); err != nil {
		msg := "prea: startup failed"
		if _, migrating := os.LookupEnv(database.MigrateEnv); migrating {
			msg = "prea: migrate failed"
		}
		if errors.Is(err, core.ErrShutdownTimeout) {
			msg = "prea: stopped"
		}
		logger.Error(msg, "error", err)
		os.Exit(1)
	}
}

func run(cfg Config) error {
	s, err := load(cfg, "")
	if err != nil {
		return err
	}
	// Runs after the server finished its in-flight requests and closed the
	// components (App.Run returns only then).
	defer s.close()

	// Before the app's own checks, like the JWT secret: a migration needs
	// only the config and the database.
	if args, ok := os.LookupEnv(database.MigrateEnv); ok {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return database.Command(ctx, s.db, s.database.Migrations, strings.Fields(args), os.Stdout)
	}

	app, err := s.app(cfg)
	if err != nil {
		return err
	}
	return s.hint(app.Run(":" + s.port))
}

// Test builds the app like Run, with the tests profile
// (config/application-tests.yaml), and returns its handler for httptest:
//
//	app := prea.Test(t, prea.Config{Modules: []*core.Module{Module}})
//	app.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
//
// It runs from the project root, the nearest directory with a go.mod above
// the test's, so config/ and migrations/ are found from any package. The app
// and the database close when the test ends.
func Test(t testing.TB, cfg Config) http.Handler {
	t.Helper()
	root, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	s, err := load(cfg, "tests")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	app, err := s.app(cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine, closer, err := app.Build(t.Context())
	if err != nil {
		t.Fatal(s.hint(err))
	}
	t.Cleanup(func() { // before s.close: cleanups run last in, first out
		if err := closer(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return engine
}

// server is the loaded config and the database, before the app is built.
type server struct {
	config   *config.Config
	port     string
	db       *pgxpool.Pool // nil without database.url
	database database.Options
}

// load reads the config of profile ("" is PREA_PROFILE), checks the required
// keys, configures the logs and gin, and opens the database.
func load(cfg Config, profile string) (*server, error) {
	c, err := config.Load(configDir, profile)
	if err != nil {
		return nil, err
	}
	s := &server{config: c}
	var mode, url string
	var logging Logging
	if err := errors.Join(
		c.Value("server.port", &s.port),
		c.Value("server.mode", &mode),
		c.Value("logging", &logging),
		c.Value("database.url", &url),
		c.Value("database", &s.database),
	); err != nil {
		return nil, err
	}
	for _, required := range [][2]string{{"server.port", s.port}, {"server.mode", mode}} {
		if required[1] == "" {
			return nil, fmt.Errorf("prea: %s is required: set it in %s", required[0], filepath.Join(configDir, "application.yaml"))
		}
	}
	if modes := []string{gin.DebugMode, gin.ReleaseMode, gin.TestMode}; !slices.Contains(modes, mode) {
		return nil, fmt.Errorf("prea: server.mode %q: use %s", mode, strings.Join(modes, ", "))
	}

	if err := logger.Configure(cmp.Or(cfg.Logging.Level, logging.Level), cmp.Or(cfg.Logging.Format, logging.Format)); err != nil {
		return nil, err
	}
	gin.SetMode(mode)
	logger.Info("prea: config loaded", "profile", c.Profile())

	if url != "" {
		if s.db, err = connect(url); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// app builds the core.App from the code and the config.
func (s *server) app(cfg Config) (core.App, error) {
	app := core.App{
		Modules:     cfg.Modules,
		Middlewares: cfg.Middlewares,
		Security:    cfg.Security,
		Config:      s.config,
		Setup:       database.Setup(s.db, s.database),
	}
	var secret string
	var proxies []string
	if err := errors.Join(
		s.config.Value("server", &app.Server),
		s.config.Value("server.trusted_proxies", &proxies),
		s.config.Value("security.jwt.secret", &secret),
	); err != nil {
		return app, err
	}
	if app.Security.TrustedProxies == nil {
		app.Security.TrustedProxies = proxies
	}
	// An app without JWT needs no secret; one that uses JWT builds a
	// security.Jwt with NewJwt. When the secret is set, check it now so a
	// short key stops the app before it serves.
	if secret != "" {
		if err := security.CheckSignKey(secret); err != nil {
			return app, err
		}
	}
	if s.db != nil {
		app.Globals = []any{s.db}
	}
	return app, nil
}

// hint explains a repository that found no database: the pool is missing
// because database.url isn't set in the profile.
func (s *server) hint(err error) error {
	var missing *di.MissingError
	if s.db == nil && errors.As(err, &missing) && missing.Type == reflect.TypeFor[*pgxpool.Pool]() {
		return fmt.Errorf("%w: set database.url in the profile (config/application*.yaml)", err)
	}
	return err
}

func (s *server) close() {
	if s.db != nil {
		s.db.Close()
	}
}

// connect fails at startup when the database is unreachable; pgxpool.New is lazy.
func connect(url string) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	return db, nil
}

// projectRoot is the nearest directory with a go.mod, from the current one up.
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("prea: no go.mod above the current directory")
		}
		dir = parent
	}
}
