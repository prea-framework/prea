package prea

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prea-framework/prea/core"
	"github.com/prea-framework/prea/database"
	"github.com/prea-framework/prea/logger"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// project creates a project in a temp dir (go.mod plus files) and moves
// into it.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/app\n"
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

const base = "server:\n  port: 8000\n  mode: test\n"

func TestRequiredKeys(t *testing.T) {
	for yaml, want := range map[string]string{
		"server:\n  mode: test\n":             "server.port is required: set it in " + filepath.Join("config", "application.yaml"),
		"server:\n  port: 8000\n":             "server.mode is required",
		"":                                    "server.port is required", // no file at all
		"server:\n  port: 8000\n  mode: prod": `server.mode "prod": use debug, release, test`,
	} {
		project(t, map[string]string{"config/application.yaml": yaml})
		if _, err := load(Config{}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", yaml, err, want)
		}
	}
}

func TestServerKeys(t *testing.T) {
	project(t, map[string]string{"config/application.yaml": base +
		"  read_timeout: 10s\n  max_header_bytes: 4096\n  trusted_proxies: [10.0.0.1]\n"})
	s, err := load(Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.port != "8000" {
		t.Fatalf("port: %q", s.port)
	}
	app, err := s.app(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if want := (core.Server{ReadTimeout: 10 * time.Second, MaxHeaderBytes: 4096}); app.Server != want {
		t.Fatalf("the yaml overrides only what it sets, the rest keeps core's defaults: %+v", app.Server)
	}
	if !slices.Equal(app.Security.TrustedProxies, []string{"10.0.0.1"}) {
		t.Fatalf("trusted proxies from the yaml: %v", app.Security.TrustedProxies)
	}
	app, _ = s.app(Config{Security: core.Security{TrustedProxies: []string{}}})
	if app.Security.TrustedProxies == nil || len(app.Security.TrustedProxies) != 0 {
		t.Fatalf("the code's proxies win, even an empty list: %v", app.Security.TrustedProxies)
	}
}

func TestLogging(t *testing.T) {
	t.Cleanup(func() { logger.Configure("", "") })
	project(t, map[string]string{"config/application.yaml": base + "logging:\n  level: debug\n"})
	enabled := func(level slog.Level) bool { return logger.Log.Enabled(context.Background(), level) }

	if _, err := load(Config{}, ""); err != nil || !enabled(slog.LevelDebug) {
		t.Fatalf("an empty Logging takes logging.level from the yaml: %v", err)
	}
	if _, err := load(Config{Logging: Logging{Level: "error"}}, ""); err != nil || enabled(slog.LevelInfo) {
		t.Fatalf("the code's level wins over the yaml: %v", err)
	}
	if _, err := load(Config{Logging: Logging{Format: "xml"}}, ""); err == nil {
		t.Fatal("an invalid format must stop the startup")
	}
}

func TestJWTSecret(t *testing.T) {
	project(t, map[string]string{"config/application.yaml": base})
	s, err := load(Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.app(Config{}); err != nil {
		t.Fatalf("an app without security.jwt.secret must start: %v", err)
	}

	project(t, map[string]string{"config/application.yaml": base + "security:\n  jwt:\n    secret: short\n"})
	s, _ = load(Config{}, "")
	if _, err := s.app(Config{}); err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("a short secret must stop the startup: %v", err)
	}
}

func TestMigrateBeforeTheApp(t *testing.T) {
	// The short secret would stop the app: the migration runs before it.
	project(t, map[string]string{"config/application.yaml": base + "security:\n  jwt:\n    secret: short\n"})
	t.Setenv(database.MigrateEnv, "status")
	if err := run(Config{}); err != nil {
		t.Fatalf("PREA_MIGRATE must run the migrations command (no migrations: nothing to do), not the app: %v", err)
	}
}

type greeter struct {
	Greeting string `value:"greeting"`
	closed   *bool
}

func (g *greeter) Register(r gin.IRouter) {
	r.GET("/hi", func(c *gin.Context) { c.String(http.StatusOK, g.Greeting) })
}

func (g *greeter) Close() error { *g.closed = true; return nil }

func TestTest(t *testing.T) {
	var closed bool
	t.Cleanup(func() { // registered first, so it runs after Test's cleanups
		if !closed {
			t.Error("Test must close the app when the test ends")
		}
	})
	dir := project(t, map[string]string{
		"config/application.yaml":       base + "greeting: base\n",
		"config/application-tests.yaml": "greeting: tests\n",
		"preas/hello/hello.go":          "package hello\n",
	})
	t.Chdir(filepath.Join(dir, "preas", "hello")) // go test runs in the package's dir

	app := Test(t, Config{Modules: []*core.Module{{Name: "hello", Providers: []any{greeter{closed: &closed}}}}})
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", "/hi", nil))
	if w.Body.String() != "tests" {
		t.Fatalf("Test must find config/ from a package dir and use the tests profile: %d %q", w.Code, w.Body)
	}
}

type note struct {
	ID   int64 `pk:"" generated:"auto_increment"`
	Text string
}

func (note) Table() string { return "prea_run_notes" }

type notes struct{ database.BaseRepository[note] }

type counter struct {
	DB    *pgxpool.Pool `inject:""`
	count int
}

func (c *counter) Init(ctx context.Context) error {
	return c.DB.QueryRow(ctx, "select count(*) from prea_run_notes").Scan(&c.count)
}

func TestTestWithDatabase(t *testing.T) {
	if os.Getenv("PREA_TEST_DATABASE_URL") == "" {
		t.Skip("PREA_TEST_DATABASE_URL not set")
	}
	project(t, map[string]string{
		"config/application.yaml":       base,
		"config/application-tests.yaml": "database:\n  url: ${PREA_TEST_DATABASE_URL}\n  schema: create-drop\n",
	})
	// The counter's Init queries the table: it gets the pool from the Globals,
	// and the table exists because Setup ran the schema before Init.
	Test(t, Config{Modules: []*core.Module{{Name: "notes", Providers: []any{notes{}, counter{}}}}})
}

func TestRepositoryWithoutDatabase(t *testing.T) {
	project(t, map[string]string{"config/application.yaml": base})
	s, err := load(Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	app, _ := s.app(Config{Modules: []*core.Module{{Name: "notes", Providers: []any{notes{}}}}})
	_, _, err = app.Build(t.Context())
	if err = s.hint(err); err == nil || !strings.Contains(err.Error(), "set database.url") {
		t.Fatalf("a repository without database.url must say which key is missing: %v", err)
	}
}
