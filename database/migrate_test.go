package database

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseMigration(t *testing.T) {
	m, err := parseMigration("0.0.2_add_active.sql", "alter table a add b int;\n\n-- prea:down\nalter table a drop b;\n")
	if err != nil {
		t.Fatal(err)
	}
	if m.version != "0.0.2" || m.name != "add_active" || m.num != 2 || m.noTx ||
		m.up != "alter table a add b int;\n\n" || m.down != "\nalter table a drop b;\n" {
		t.Fatalf("%+v", m)
	}
	if m, _ := parseMigration("20261013120000_x.sql", "\n-- prea:no-transaction\ncreate index concurrently i on a (b);"); !m.noTx || m.num != 20261013120000 ||
		strings.Contains(m.up, "prea:") || m.down != "" {
		t.Fatalf("no-transaction: %+v", m)
	}
	if m, _ := parseMigration("0.0.1_x.sql", "select 1; -- prea:down is only a marker on its own line"); m.down != "" {
		t.Fatalf("inline marker: %+v", m)
	}

	for file, want := range map[string]string{
		"create_users.sql":    "expected <version>_<name>.sql",
		"0.0.1_CreateX.sql":   "expected <version>_<name>.sql",
		"0.0_x.sql":           "expected <version>_<name>.sql",
		"0.0.0_x.sql":         "must be greater than zero",
		"1.1000000.0_x.sql":   "each part must be below 1000000",
		"0.0.1_empty.sql":     "nothing to run",
		"0.0.2_emptydown.sql": "the -- prea:down section is empty",
	} {
		content := "select 1;"
		switch file {
		case "0.0.1_empty.sql":
			content = "  \n-- prea:down\nselect 1;"
		case "0.0.2_emptydown.sql":
			content = "select 1;\n-- prea:down\n\n"
		}
		if _, err := parseMigration(file, content); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", file, err, want)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	var prev int64
	for _, v := range []string{"0.0.1", "0.0.9", "0.0.10", "0.1.0", "0.99.999999", "1.0.0", "999999.0.0"} {
		n, err := versionNumber(v)
		if err != nil || n <= prev {
			t.Fatalf("%s: %d after %d (%v)", v, n, prev, err)
		}
		prev = n
	}
}

func TestReadMigrations(t *testing.T) {
	write := func(dir string, files map[string]string) string {
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	dir := write(t.TempDir(), map[string]string{"0.0.10_c.sql": "select 3;", "0.0.9_b.sql": "select 2;", "0.0.1_a.sql": "select 1;", "README.md": "notes"})
	migs, err := readMigrations(dir)
	if err != nil || len(migs) != 3 || migs[0].name != "a" || migs[2].String() != "0.0.10_c" {
		t.Fatalf("ordered by version: %v %v", migs, err)
	}
	if migs, err := readMigrations(filepath.Join(dir, "missing")); err != nil || migs != nil {
		t.Fatalf("a missing dir has no migrations: %v %v", migs, err)
	}

	for want, files := range map[string]map[string]string{
		"have the same version":         {"0.0.1_a.sql": "select 1;", "1_b.sql": "select 1;"},
		"mix semver and timestamp":      {"0.0.1_a.sql": "select 1;", "20261013120000_b.sql": "select 1;"},
		"expected <version>_<name>.sql": {"bad.sql": "select 1;", "0.0.1_a.sql": ""},
		"0.0.1_a.sql: nothing to run":   {"bad.sql": "select 1;", "0.0.1_a.sql": ""}, // every file error at once
	} {
		if _, err := readMigrations(write(t.TempDir(), files)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want %q", err, want)
		}
	}
}

func TestNextVersion(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	semver := []migration{{version: "0.0.1"}, {version: "0.1.9"}}
	stamp := []migration{{version: "20261013120000"}}
	for _, tc := range []struct {
		migs   []migration
		format string
		want   string
	}{
		{nil, "", "0.0.1"},
		{semver, "semver", "0.1.10"},
		{nil, "timestamp", "20261013150000"},
		{stamp, "timestamp", "20261013150000"},
		{stamp, "", "set database.migrations.version: timestamp"},
		{semver, "timestamp", "set database.migrations.version: semver"},
		{nil, "date", `migrations.version "date" is not semver or timestamp`},
	} {
		got, err := nextVersion(tc.migs, tc.format, now)
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%v %q: got %q, want %q", tc.migs, tc.format, got, tc.want)
		}
	}
}

func TestGooseFormat(t *testing.T) {
	m := migration{up: "create function f() returns int as $$ select 1; $$ language sql;", down: "drop function f;", noTx: true}
	want := `-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
create function f() returns int as $$ select 1; $$ language sql;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
drop function f;
-- +goose StatementEnd
`
	if got := string(m.goose()); got != want {
		t.Fatalf("got\n%s", got)
	}
	if got := string(migration{up: "select 1;"}.goose()); strings.Contains(got, "Down") || strings.Contains(got, "NO TRANSACTION") {
		t.Fatalf("no down, in a transaction: %s", got)
	}
}

func TestMigrateCommands(t *testing.T) {
	ctx := t.Context()
	pool := testDB(t)
	if _, err := pool.Exec(ctx, "drop table if exists prea_migrations, mig_a, mig_b, mig_c; drop function if exists mig_f"); err != nil {
		t.Fatal(err)
	}
	opts := Migrations{Dir: filepath.Join(t.TempDir(), "migrations")}
	run := func(args ...string) (string, error) {
		var out strings.Builder
		err := Command(ctx, pool, opts, args, &out)
		return out.String(), err
	}
	mustRun := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	write := func(file, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(opts.Dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tableExists := func(name string) bool {
		var ok bool
		pool.QueryRow(ctx, "select to_regclass($1) is not null", name).Scan(&ok)
		return ok
	}

	if out := mustRun("status"); !strings.Contains(out, "no migrations") {
		t.Fatalf("empty dir: %q", out)
	}
	if out := mustRun("create", "a"); !strings.Contains(out, "0.0.1_a.sql") {
		t.Fatalf("create: %q", out)
	}
	if _, err := run("status"); err == nil || !strings.Contains(err.Error(), "0.0.1_a.sql: nothing to run") {
		t.Fatalf("a created file must be filled in: %v", err)
	}
	// the whole file runs as written: several statements and a $$ body
	write("0.0.1_a.sql", `create table mig_a (id int);
create function mig_f() returns int as $$ select 1; $$ language sql;
-- prea:down
drop function mig_f;
drop table mig_a;
`)
	mustRun("create", "b")
	write("0.0.2_b.sql", "create table mig_b (id int);")

	if out := mustRun("validate"); out != "migrations ok: 2\n" {
		t.Fatalf("validate: %q", out)
	}
	if out := mustRun("up", "0.0.1"); !strings.Contains(out, "applied 0.0.1_a") || strings.Contains(out, "0.0.2") {
		t.Fatalf("up to a version: %q", out)
	}
	if out := mustRun("status"); !strings.Contains(out, "0.0.1_a") || !strings.Contains(out, "applied 2") || !strings.Contains(out, "0.0.2_b") || !strings.Contains(out, "pending") {
		t.Fatalf("status: %q", out)
	}
	if out := mustRun("up"); !strings.Contains(out, "applied 0.0.2_b") {
		t.Fatalf("up: %q", out)
	}
	if out := mustRun("up"); out != "no pending migrations\n" {
		t.Fatalf("up again: %q", out)
	}

	if _, err := run("down"); err == nil || !strings.Contains(err.Error(), "0.0.2_b has no -- prea:down section") || !tableExists("mig_b") {
		t.Fatalf("down without a section must refuse and keep the table: %v", err)
	}
	write("0.0.2_b.sql", "create table mig_b (id int);\n-- prea:down\ndrop table mig_b;")
	if out := mustRun("down", "2"); !strings.Contains(out, "undid 0.0.2_b") || !strings.Contains(out, "undid 0.0.1_a") || tableExists("mig_a") || tableExists("mig_b") {
		t.Fatalf("down 2: %q", out)
	}

	// a failing migration is rolled back and stops the ones after it
	write("0.0.3_c.sql", "create table mig_c (id int);\nselect 1/0;")
	write("0.0.4_d.sql", "select 1;")
	out, err := run("up")
	if err == nil || !strings.Contains(err.Error(), "migration 0.0.3_c") || !strings.Contains(err.Error(), "division by zero") || tableExists("mig_c") {
		t.Fatalf("failing migration: %v", err)
	}
	if !strings.Contains(out, "applied 0.0.2_b") || strings.Contains(out, "0.0.4") {
		t.Fatalf("the ones before it stay applied: %q", out)
	}

	// two instances starting together apply each migration once (advisory lock)
	write("0.0.3_c.sql", "create table mig_c (id int);")
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, err := run("up"); errs <- err }()
	}
	if err := errors.Join(<-errs, <-errs); err != nil {
		t.Fatal(err)
	}
	var n int
	pool.QueryRow(ctx, "select count(*) from prea_migrations where version_id > 0").Scan(&n)
	if n != 4 {
		t.Fatalf("every migration once: %d rows", n)
	}

	for _, args := range [][]string{{}, {"create"}, {"create", "Bad-Name"}, {"down", "zero"}, {"up", "9.9.9"}, {"fly"}} {
		if _, err := run(args...); err == nil {
			t.Errorf("%v must fail", args)
		}
	}
}

type note struct {
	ID   int64  `pk:"" generated:"auto_increment"`
	Text string `unique:""`
}

func (note) Table() string { return "notes" }

// note with a new column: the same table, a later version of the model.
type noteV2 struct {
	ID    int64  `pk:"" generated:"auto_increment"`
	Text  string `unique:""`
	Color *string
}

func (noteV2) Table() string { return "notes" }

func TestStart(t *testing.T) {
	ctx := t.Context()
	pool := testDB(t)
	if _, err := pool.Exec(ctx, "drop table if exists prea_migrations, notes"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "migrations")
	opts := Options{Schema: SchemaValidate, Migrations: Migrations{Dir: dir, FromModels: true, OnStart: true}}
	start := func(opts Options, model any) {
		t.Helper()
		if _, err := Setup(pool, opts)(ctx, []any{model}); err != nil {
			t.Fatal(err)
		}
	}
	files := func() []string {
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	// an empty database: the whole schema is generated, applied, then validated
	start(opts, &BaseRepository[note]{DB: pool})
	if got := files(); len(got) != 1 || got[0] != "0.0.1_create_notes_table.sql" {
		t.Fatalf("generated: %v", got)
	}
	start(opts, &BaseRepository[note]{DB: pool})
	if got := files(); len(got) != 1 {
		t.Fatalf("in sync: no new file: %v", got)
	}

	// the model changes: the next start generates and applies only the difference
	start(opts, &BaseRepository[noteV2]{DB: pool})
	content, _ := os.ReadFile(filepath.Join(dir, "0.0.2_alter_notes_table.sql"))
	if !strings.Contains(string(content), `alter table "notes" add column "color" text;`) ||
		!strings.Contains(string(content), "-- prea:down\n"+`alter table "notes" drop column "color";`) {
		t.Fatalf("0.0.2:\n%s", content)
	}
	if err := Command(ctx, pool, opts.Migrations, []string{"down"}, io.Discard); err != nil {
		t.Fatalf("the generated down undoes it: %v", err)
	}
	if _, err := SyncSchema(ctx, SchemaValidate, []any{&BaseRepository[note]{DB: pool}}); err != nil {
		t.Fatalf("back to the first model: %v", err)
	}

	// with migrations pending, nothing is generated (they may hold the change)
	start(Options{Migrations: Migrations{Dir: dir, FromModels: true}}, &BaseRepository[noteV2]{DB: pool})
	if got := files(); len(got) != 2 {
		t.Fatalf("pending: no new file: %v", got)
	}

	for _, tc := range []struct {
		opts Options
		db   *pgxpool.Pool
		want string
	}{
		{Options{Schema: SchemaUpdate, Migrations: Migrations{FromModels: true}}, pool, "use schema none or validate with it, not update"},
		{Options{Migrations: Migrations{OnStart: true}}, nil, "migrations need a database"},
	} {
		if _, err := Setup(tc.db, tc.opts)(ctx, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("got %v, want %q", err, tc.want)
		}
	}
}

func TestGeneratedName(t *testing.T) {
	for _, tc := range []struct {
		changes []change
		want    string
	}{
		{[]change{{table: "task", up: "create table", create: true}, {table: "task", up: "create index"}}, "create_task_table"},
		{[]change{{table: "task", up: "alter table"}, {table: "task", up: "-- dropped column"}}, "alter_task_table"},
		{[]change{{table: "list", up: "create table", create: true}, {table: "task", up: "alter table"}}, "create_list_table_and_alter_task_table"},
		{[]change{{table: "list", up: "create table", create: true}, {table: "item", up: "create table", create: true}}, "create_list_item_tables"},
		{[]change{{table: "task", up: "alter table"}, {table: "user", up: "-- only a comment"}}, "alter_task_table"},
		{[]change{{table: "app.Task", up: "create schema"}, {table: "app.Task", up: "create table", create: true}}, "create_app_task_table"},
	} {
		if got := generatedName(tc.changes); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.changes, got, tc.want)
		}
	}
}
