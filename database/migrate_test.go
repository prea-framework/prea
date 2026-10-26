package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestMigrateCommands(t *testing.T) {
	m := newMigrator(t, "mig_a, mig_b, mig_c; drop function if exists mig_f")
	ctx := t.Context()
	tableExists := func(name string) bool {
		var ok bool
		m.pool.QueryRow(ctx, "select to_regclass($1) is not null", name).Scan(&ok)
		return ok
	}

	if out := m.mustRun("status"); !strings.Contains(out, "no migrations") {
		t.Fatalf("empty dir: %q", out)
	}
	if out := m.mustRun("create", "a"); !strings.Contains(out, "0.0.1_a.sql") {
		t.Fatalf("create: %q", out)
	}
	if _, err := m.run("status"); err == nil || !strings.Contains(err.Error(), "0.0.1_a.sql: nothing to run") {
		t.Fatalf("a created file must be filled in: %v", err)
	}
	// the whole file runs as written: several statements and a $$ body
	m.write("0.0.1_a.sql", `create table mig_a (id int);
create function mig_f() returns int as $$ select 1; $$ language sql;
-- prea:down
drop function mig_f;
drop table mig_a;
`)
	m.mustRun("create", "b")
	m.write("0.0.2_b.sql", "create table mig_b (id int);")

	if out := m.mustRun("validate"); out != "migrations ok: 2\n" {
		t.Fatalf("validate: %q", out)
	}
	if out := m.mustRun("up", "0.0.1"); !strings.Contains(out, "applied 0.0.1_a") || strings.Contains(out, "0.0.2") {
		t.Fatalf("up to a version: %q", out)
	}
	if out := m.mustRun("status"); !strings.Contains(out, "0.0.1_a") || !strings.Contains(out, "applied 2") || !strings.Contains(out, "0.0.2_b") || !strings.Contains(out, "pending") {
		t.Fatalf("status: %q", out)
	}
	if out := m.mustRun("up"); !strings.Contains(out, "applied 0.0.2_b") {
		t.Fatalf("up: %q", out)
	}
	if out := m.mustRun("up"); out != "no pending migrations\n" {
		t.Fatalf("up again: %q", out)
	}

	if _, err := m.run("down"); err == nil || !strings.Contains(err.Error(), "0.0.2_b has no -- prea:down section") || !tableExists("mig_b") {
		t.Fatalf("down without a section must refuse and keep the table: %v", err)
	}
	m.write("0.0.2_b.sql", "create table mig_b (id int);\n-- prea:down\ndrop table mig_b;")
	if out := m.mustRun("down", "2"); !strings.Contains(out, "undid 0.0.2_b") || !strings.Contains(out, "undid 0.0.1_a") || tableExists("mig_a") || tableExists("mig_b") {
		t.Fatalf("down 2: %q", out)
	}

	// a failing migration is rolled back and stops the ones after it
	m.write("0.0.3_c.sql", "create table mig_c (id int);\nselect 1/0;")
	m.write("0.0.4_d.sql", "select 1;")
	out, err := m.run("up")
	if err == nil || !strings.Contains(err.Error(), "migration 0.0.3_c") || !strings.Contains(err.Error(), "division by zero") || tableExists("mig_c") {
		t.Fatalf("failing migration: %v", err)
	}
	if !strings.Contains(out, "applied 0.0.2_b") || strings.Contains(out, "0.0.4") {
		t.Fatalf("the ones before it stay applied: %q", out)
	}

	// two instances starting together apply each migration once (advisory
	// lock); the sleep makes them overlap
	m.write("0.0.3_c.sql", "select pg_sleep(0.3);\ncreate table mig_c (id int);")
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, err := m.run("up"); errs <- err }()
	}
	if err := errors.Join(<-errs, <-errs); err != nil {
		t.Fatal(err)
	}
	var n int
	m.pool.QueryRow(ctx, "select count(*) from prea_migrations").Scan(&n)
	if n != 4 {
		t.Fatalf("every migration once: %d rows", n)
	}

	for _, args := range [][]string{{}, {"create"}, {"create", "Bad-Name"}, {"down", "zero"}, {"up", "9.9.9"}, {"fly"}} {
		if _, err := m.run(args...); err == nil {
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

// migrator runs migrations on a clean database and a temp dir, for the tests
// below.
type migrator struct {
	t    *testing.T
	pool *pgxpool.Pool
	opts Migrations
}

// gooseTable creates prea_migrations as goose kept it, up to Preá v0.0.4.
const gooseTable = `create table prea_migrations (id serial primary key, version_id bigint not null, is_applied boolean not null, tstamp timestamp not null default now());
`

func newMigrator(t *testing.T, tables string) *migrator {
	pool := testDB(t)
	if _, err := pool.Exec(t.Context(), "drop table if exists "+strings.Trim("prea_migrations, "+tables, ", ")); err != nil {
		t.Fatal(err)
	}
	m := &migrator{t, pool, Migrations{Dir: filepath.Join(t.TempDir(), "migrations")}}
	os.MkdirAll(m.opts.Dir, 0o755)
	return m
}

func (m *migrator) write(file, content string) {
	m.t.Helper()
	if err := os.WriteFile(filepath.Join(m.opts.Dir, file), []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *migrator) remove(file string) {
	m.t.Helper()
	if err := os.Remove(filepath.Join(m.opts.Dir, file)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *migrator) run(args ...string) (string, error) {
	var out strings.Builder
	err := Command(m.t.Context(), m.pool, m.opts, args, &out)
	return out.String(), err
}

func (m *migrator) mustRun(args ...string) string {
	m.t.Helper()
	out, err := m.run(args...)
	if err != nil {
		m.t.Fatalf("%v: %v", args, err)
	}
	return out
}

// mustFail runs args and wants an error with each of want.
func (m *migrator) mustFail(want []string, args ...string) {
	m.t.Helper()
	_, err := m.run(args...)
	for _, w := range want {
		if err == nil || !strings.Contains(err.Error(), w) {
			m.t.Fatalf("%v: want an error with %q, got %v", args, w, err)
		}
	}
}

func (m *migrator) exists(table string) bool {
	var ok bool
	m.pool.QueryRow(m.t.Context(), "select to_regclass($1) is not null", table).Scan(&ok)
	return ok
}

// rows is prea_migrations as "version name", in the order they were applied.
func (m *migrator) rows() []string {
	m.t.Helper()
	rows, _ := m.pool.Query(m.t.Context(), "select version || ' ' || name from prea_migrations order by applied_at, length(version), version")
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		m.t.Fatal(err)
	}
	return got
}

func TestMigrationsTable(t *testing.T) {
	m := newMigrator(t, "mig_x, mig_y")
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);")
	m.write("0.0.2_create_y.sql", "create table mig_y (id int);")
	m.mustRun("up")

	// the version as written, the name, the checksum of the up and the time
	var version, name, checksum string
	var at time.Time
	if err := m.pool.QueryRow(t.Context(), "select version, name, checksum, applied_at from prea_migrations where version = '0.0.2'").Scan(&version, &name, &checksum, &at); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("create table mig_y (id int);"))
	if version != "0.0.2" || name != "create_y" || checksum != hex.EncodeToString(want[:]) || time.Since(at) > time.Minute {
		t.Fatalf("row: %q %q %q %v", version, name, checksum, at)
	}
	var typ string
	m.pool.QueryRow(t.Context(), "select data_type from information_schema.columns where table_name = 'prea_migrations' and column_name = 'version'").Scan(&typ)
	if typ != "text" {
		t.Fatalf("version is %s, want text", typ)
	}
}

func TestMigrationsTimestampVersions(t *testing.T) {
	m := newMigrator(t, "mig_x")
	m.opts.Version = "timestamp"
	m.write("20261013120000_create_x.sql", "create table mig_x (id int);")
	m.mustRun("up")
	if got := m.rows(); !slices.Equal(got, []string{"20261013120000 create_x"}) {
		t.Fatalf("rows: %v", got)
	}
	m.remove("20261013120000_create_x.sql")
	m.mustFail([]string{"20261013120000_create_x: applied, but its file is missing"}, "status")
}

// Deleting, renaming, editing or replacing an applied migration doesn't undo
// it, so every command, and the startup, stops until the file is back.
func TestAppliedMigrationsMatchFiles(t *testing.T) {
	m := newMigrator(t, "mig_x, mig_y, mig_z")
	y := "create table mig_y (\n\tid int\n);\n-- prea:down\ndrop table mig_y;\n"
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);")
	m.write("0.0.2_create_y.sql", y)
	m.mustRun("up")

	// deleted: every command stops, create too (it would take 0.0.2 again)
	m.remove("0.0.2_create_y.sql")
	missing := []string{"0.0.2_create_y: applied, but its file is missing", "Restore the file"}
	for _, args := range [][]string{{"up"}, {"down"}, {"status"}, {"create", "z"}} {
		m.mustFail(missing, args...)
	}
	if _, err := os.Stat(filepath.Join(m.opts.Dir, "0.0.2_z.sql")); err == nil {
		t.Fatal("create must not write a file")
	}
	for _, opts := range []Migrations{{Dir: m.opts.Dir, FromModels: true}, {Dir: m.opts.Dir, OnStart: true}} {
		if _, err := Setup(m.pool, Options{Migrations: opts})(t.Context(), nil); err == nil || !strings.Contains(err.Error(), missing[0]) {
			t.Fatalf("startup %+v: %v", opts, err)
		}
	}
	m.remove("0.0.1_create_x.sql") // every file deleted
	m.mustFail([]string{"0.0.1_create_x: applied, but its file is missing", "0.0.2_create_y: applied"}, "up")

	// replaced: a new file takes the applied version
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);")
	m.write("0.0.2_create_z.sql", "create table mig_z (id int);")
	m.mustFail([]string{"0.0.2_create_y: applied, but its version is now 0.0.2_create_z.sql"}, "up")
	m.remove("0.0.2_create_z.sql")

	// renamed, same SQL
	m.write("0.0.2_make_y.sql", y)
	m.mustFail([]string{"0.0.2_create_y: applied, but its version is now 0.0.2_make_y.sql"}, "up")
	m.remove("0.0.2_make_y.sql")

	// edited up
	m.write("0.0.2_create_y.sql", "create table mig_y (id bigint);\n-- prea:down\ndrop table mig_y;\n")
	m.mustFail([]string{"0.0.2_create_y: applied, but its SQL changed since"}, "up")

	// restored with Windows line endings and a blank line: the same SQL
	m.write("0.0.2_create_y.sql", "\r\n"+strings.ReplaceAll(y, "\n", "\r\n"))
	m.mustRun("status")

	// the down can change: it hasn't run
	m.write("0.0.2_create_y.sql", "create table mig_y (\n\tid int\n);\n-- prea:down\ndrop table if exists mig_y;\n")
	m.mustRun("up")

	// undone, the file can change or go: its row is gone
	if out := m.mustRun("down"); !strings.Contains(out, "undid 0.0.2_create_y") {
		t.Fatalf("down: %q", out)
	}
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 create_x"}) {
		t.Fatalf("after down: %v", got)
	}
	m.remove("0.0.2_create_y.sql")
	m.write("0.0.2_create_z.sql", "create table mig_z (id int);")
	if out := m.mustRun("up"); !strings.Contains(out, "applied 0.0.2_create_z") {
		t.Fatalf("a new 0.0.2: %q", out)
	}
}

// Migrations only run forward: a pending one older than the highest applied
// version, as after merging two branches, stops every command and the
// startup until it gets a higher version; down undoes from the highest.
func TestMigrationsForwardOnly(t *testing.T) {
	m := newMigrator(t, "mig_x, mig_y, mig_z")
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);\n-- prea:down\ndrop table mig_x;")
	m.write("0.0.3_create_z.sql", "create table mig_z (id int);\n-- prea:down\ndrop table mig_z;")
	m.mustRun("up")

	m.write("0.0.2_create_y.sql", "create table mig_y (id int);\n-- prea:down\ndrop table mig_y;")
	older := []string{"0.0.2_create_y: pending, but older than the last applied migration, 0.0.3_create_z", "a version above 0.0.3"}
	for _, args := range [][]string{{"up"}, {"up", "0.0.2"}, {"status"}, {"down"}, {"create", "w"}} {
		m.mustFail(older, args...)
	}
	if _, err := Setup(m.pool, Options{Migrations: Migrations{Dir: m.opts.Dir, OnStart: true}})(t.Context(), nil); err == nil || !strings.Contains(err.Error(), older[0]) {
		t.Fatalf("startup: %v", err)
	}
	var y bool
	m.pool.QueryRow(t.Context(), "select to_regclass('mig_y') is not null").Scan(&y)
	if y {
		t.Fatal("0.0.2 must not run")
	}

	// a higher version runs
	m.remove("0.0.2_create_y.sql")
	m.write("0.0.4_create_y.sql", "create table mig_y (id int);\n-- prea:down\ndrop table mig_y;")
	if out := m.mustRun("up"); !strings.Contains(out, "applied 0.0.4_create_y") {
		t.Fatalf("up: %q", out)
	}

	// down from the highest version: what's left pending stays above
	if out := m.mustRun("down", "2"); !strings.Contains(out, "undid 0.0.4_create_y") || !strings.Contains(out, "undid 0.0.3_create_z") {
		t.Fatalf("down 2: %q", out)
	}
	m.mustRun("status")
}

// goose allowed migrations out of order, so a converted database can have
// 0.0.3 applied before 0.0.2: down still undoes the highest version, and
// nothing is left pending below an applied one.
func TestMigrationsFromGooseOutOfOrder(t *testing.T) {
	m := newMigrator(t, "mig_x, mig_y, mig_z")
	if _, err := m.pool.Exec(t.Context(), `create table prea_migrations (id serial primary key, version_id bigint not null, is_applied boolean not null, tstamp timestamp not null default now());
		insert into prea_migrations (version_id, is_applied, tstamp) values (0, true, now()), (1, true, now()), (3, true, now() + interval '1 second'), (2, true, now() + interval '2 seconds');
		create table mig_x (id int); create table mig_y (id int); create table mig_z (id int)`); err != nil {
		t.Fatal(err)
	}
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);\n-- prea:down\ndrop table mig_x;")
	m.write("0.0.2_create_y.sql", "create table mig_y (id int);\n-- prea:down\ndrop table mig_y;")
	m.write("0.0.3_create_z.sql", "create table mig_z (id int);\n-- prea:down\ndrop table mig_z;")
	if out := m.mustRun("down"); !strings.Contains(out, "undid 0.0.3_create_z") {
		t.Fatalf("down: %q", out)
	}
	if out := m.mustRun("status"); !strings.Contains(out, "0.0.3_create_z") || !strings.Contains(out, "pending") {
		t.Fatalf("status: %q", out)
	}
}

func TestMigrationNoTransaction(t *testing.T) {
	m := newMigrator(t, "mig_x")
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);")
	m.write("0.0.2_index_x.sql", "-- prea:no-transaction\ncreate index concurrently mig_x_id on mig_x (id);\n-- prea:down\ndrop index concurrently mig_x_id;")
	m.mustRun("up")
	m.mustRun("down")
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 create_x"}) {
		t.Fatalf("rows: %v", got)
	}
}

// Databases migrated by v0.0.4 and before have goose's prea_migrations, with
// a number per version. The first run converts it, taking the names from the
// files; a version without a file is reported.
func TestMigrationsFromGoose(t *testing.T) {
	m := newMigrator(t, "mig_x")
	ctx := t.Context()
	if _, err := m.pool.Exec(ctx, `create table prea_migrations (id serial primary key, version_id bigint not null, is_applied boolean not null, tstamp timestamp not null default now());
		insert into prea_migrations (version_id, is_applied) values (0, true), (1, true), (2, true);
		create table mig_x (id int)`); err != nil {
		t.Fatal(err)
	}
	m.write("0.0.1_create_x.sql", "create table mig_x (id int);")
	m.write("0.0.2_alter_x.sql", "alter table mig_x add column name text;")
	m.write("0.0.3_alter_x_again.sql", "alter table mig_x add column note text;")
	if out := m.mustRun("up"); !strings.Contains(out, "applied 0.0.3_alter_x_again") || strings.Contains(out, "0.0.2") {
		t.Fatalf("only 0.0.3 is pending: %q", out)
	}
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 create_x", "0.0.2 alter_x", "0.0.3 alter_x_again"}) {
		t.Fatalf("converted rows: %v", got)
	}

	// a goose version without a file
	m.pool.Exec(ctx, `drop table prea_migrations; create table prea_migrations (id serial primary key, version_id bigint not null, is_applied boolean not null, tstamp timestamp not null default now());
		insert into prea_migrations (version_id, is_applied) values (0, true), (1, true), (7, true)`)
	m.mustFail([]string{"0.0.7: applied, but its file is missing"}, "status")
	m.mustFail([]string{"0.0.7: applied, but its file is missing"}, "status") // converted once, still reported

	// down undoes the highest version first, by number (0.0.10 after 0.0.9),
	// not as text
	m.pool.Exec(ctx, `drop table prea_migrations, mig_x; create table prea_migrations (id serial primary key, version_id bigint not null, is_applied boolean not null, tstamp timestamp not null default now());
		insert into prea_migrations (version_id, is_applied) values (0, true), (9, true), (10, true);
		create table mig_x (id int, name text)`)
	for _, f := range []string{"0.0.1_create_x.sql", "0.0.2_alter_x.sql", "0.0.3_alter_x_again.sql"} {
		m.remove(f)
	}
	m.write("0.0.9_create_x.sql", "create table mig_x (id int);\n-- prea:down\ndrop table mig_x;")
	m.write("0.0.10_alter_x.sql", "alter table mig_x add column name text;\n-- prea:down\nalter table mig_x drop column name;")
	if out := m.mustRun("down"); !strings.Contains(out, "undid 0.0.10_alter_x") {
		t.Fatalf("down: %q", out)
	}
}
