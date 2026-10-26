package database

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"github.com/prea-framework/prea/logger"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrations is the database.migrations block of the config.
type Migrations struct {
	Dir        string `yaml:"dir"`         // default migrations
	Version    string `yaml:"version"`     // semver (default) or timestamp
	OnStart    bool   `yaml:"on_start"`    // apply the pending migrations at startup
	FromModels bool   `yaml:"from_models"` // write a migration with the models' diff at startup
}

func (o Migrations) dir() string { return cmp.Or(o.Dir, "migrations") }

var errNoDatabase = errors.New("database: migrations need a database (the pool is nil)")

// MigrateEnv holds a prea migrate command, "<command> [args]": the CLI builds
// the app and runs it with this set, and the app runs Command with its own
// config and pool instead of serving.
const MigrateEnv = "PREA_MIGRATE"

// Options is the database block of the config.
type Options struct {
	Schema     string     `yaml:"schema"` // see SyncSchema
	Migrations Migrations `yaml:"migrations"`
}

// Setup prepares the database at startup, as core.App.Setup, in order:
//  1. migrations.from_models: writes a migration with the diff between the
//     models and the database, unless migrations are pending;
//  2. migrations.on_start: applies the pending migrations;
//  3. schema: syncs the models (see SyncSchema), whose drop it returns.
//
// db is the app's main pool, the one the migrations run on; components are
// the app's, where the repositories are.
func Setup(db *pgxpool.Pool, opts Options) func(ctx context.Context, components []any) (drop func(context.Context) error, err error) {
	return func(ctx context.Context, components []any) (func(context.Context) error, error) {
		m := opts.Migrations
		if m.FromModels && slices.Contains([]string{SchemaUpdate, SchemaCreate, SchemaCreateDrop}, opts.Schema) {
			return nil, fmt.Errorf("database: migrations.from_models writes the models' changes as migrations: use schema none or validate with it, not %s", opts.Schema)
		}
		if (m.FromModels || m.OnStart) && db == nil {
			return nil, errNoDatabase
		}
		if m.FromModels {
			if err := generateMigration(ctx, db, m, components); err != nil {
				return nil, err
			}
		}
		if m.OnStart {
			if err := Command(ctx, db, m, []string{"up"}, logWriter{}); err != nil {
				return nil, err
			}
		}
		return SyncSchema(ctx, opts.Schema, components)
	}
}

// generateMigration writes <next version>_<name>.sql, named by
// generatedName, with what the database
// lacks to match the models. With migrations pending it does nothing: they
// may already have these changes.
func generateMigration(ctx context.Context, db *pgxpool.Pool, m Migrations, components []any) error {
	migs, err := readMigrations(m.dir())
	if err != nil {
		return err
	}
	if len(migs) > 0 {
		r, err := newRunner(db, migs)
		if err != nil {
			return err
		}
		pending, err := r.p.HasPending(ctx)
		r.p.Close()
		if err != nil {
			return err
		}
		if pending {
			logger.Info("database: migrations are pending: apply them before generating one from the models")
			return nil
		}
	}

	// ponytail: only the models on db; repositories on other pools aren't migrated
	_, tables, err := models(components)
	if err != nil {
		return err
	}
	changes, err := diff(ctx, db, tables[db])
	if err != nil {
		return err
	}
	sql, ok := generated(changes)
	if !ok {
		if len(changes) == 0 {
			logger.Info("database: the models and the database are in sync")
		}
		for _, c := range changes { // only what a migration must not do on its own
			logger.Info("database: " + c.msg + ": left as it is")
		}
		return nil
	}
	path, err := writeMigration(m, migs, generatedName(changes), sql)
	if err != nil {
		return err
	}
	logger.Success("database: generated a migration from the models: review it before committing", "file", path)
	return nil
}

// logWriter logs each line Command writes.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logger.Info("database: " + strings.TrimSpace(string(p)))
	return len(p), nil
}

// Command runs a migrations command, the ones of prea migrate:
//
//	create <name>   a new file with the next version
//	up [version]    applies the pending migrations, or up to version
//	down [n]        undoes the last n applied (default 1)
//	status          lists every migration: applied or pending
//	validate        checks the files without touching the database
//
// The migrations run with goose, which keeps them in prea_migrations (created
// before the first one) and holds an advisory lock while they run.
func Command(ctx context.Context, db *pgxpool.Pool, opts Migrations, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("database: migrate needs a command: create, up, down, status or validate")
	}
	migs, err := readMigrations(opts.dir())
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "create":
		if len(args) != 1 || !migrationName.MatchString(args[0]) {
			return errors.New("database: create needs a snake_case name, e.g. create add_users_active")
		}
		path, err := writeMigration(opts, migs, args[0], "-- The SQL to run. Below a \"-- prea:down\" line, the SQL that undoes it.\n")
		if err == nil {
			fmt.Fprintln(out, "created", path)
		}
		return err
	case "validate":
		fmt.Fprintf(out, "migrations ok: %d\n", len(migs))
		return nil
	case "up", "down", "status":
	default:
		return fmt.Errorf("database: unknown migrate command %q", cmd)
	}
	if len(migs) == 0 {
		fmt.Fprintf(out, "no migrations in %s\n", opts.dir())
		return nil
	}
	if db == nil {
		return errNoDatabase
	}
	r, err := newRunner(db, migs)
	if err != nil {
		return err
	}
	defer r.p.Close()

	switch cmd {
	case "up":
		if len(args) > 1 {
			return errors.New("database: up takes at most a version")
		}
		return r.up(ctx, args, out)
	case "down":
		n := 1
		if len(args) > 0 {
			if n, err = strconv.Atoi(args[0]); err != nil || n < 1 || len(args) > 1 {
				return errors.New("database: down takes the number of migrations to undo, e.g. down 2")
			}
		}
		return r.down(ctx, n, out)
	default:
		statuses, err := r.p.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			state := "pending"
			if s.State == goose.StateApplied {
				state = "applied " + s.AppliedAt.Local().Format(time.DateTime)
			}
			fmt.Fprintf(out, "%-40s %s\n", r.byNum[s.Source.Version], state)
		}
		return nil
	}
}

type runner struct {
	p     *goose.Provider
	byNum map[int64]migration
}

// newRunner hands migs to goose in its format, in memory.
func newRunner(db *pgxpool.Pool, migs []migration) (*runner, error) {
	fsys := fstest.MapFS{}
	byNum := map[int64]migration{}
	for _, m := range migs {
		fsys[fmt.Sprintf("%d_%s.sql", m.num, m.name)] = &fstest.MapFile{Data: m.goose()}
		byNum[m.num] = m
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(db), fsys,
		goose.WithTableName("prea_migrations"),
		goose.WithSessionLocker(locker),
		goose.WithAllowOutofOrder(true), // a pending migration older than the last applied (after a merge) still runs
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("database: migrations: %w", err)
	}
	return &runner{p, byNum}, nil
}

func (r *runner) up(ctx context.Context, to []string, out io.Writer) error {
	var results []*goose.MigrationResult
	var err error
	if len(to) == 0 {
		results, err = r.p.Up(ctx)
	} else {
		num, _ := versionNumber(to[0])
		if _, ok := r.byNum[num]; !ok {
			return fmt.Errorf("database: no migration with version %s", to[0])
		}
		results, err = r.p.UpTo(ctx, num)
	}
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		results = partial.Applied
		err = fmt.Errorf("database: migration %s: %w", r.byNum[partial.Failed.Source.Version], partial.Err)
	}
	for _, res := range results {
		fmt.Fprintf(out, "applied %s (%s)\n", r.byNum[res.Source.Version], res.Duration.Round(time.Millisecond))
	}
	if err == nil && len(results) == 0 {
		fmt.Fprintln(out, "no pending migrations")
	}
	return err
}

// down undoes the most recently applied migrations, one by one; it stops
// before one without a -- prea:down section.
func (r *runner) down(ctx context.Context, n int, out io.Writer) error {
	for range n {
		statuses, err := r.p.Status(ctx)
		if err != nil {
			return err
		}
		var last *goose.MigrationStatus
		for _, s := range statuses {
			if s.State == goose.StateApplied && (last == nil || !s.AppliedAt.Before(last.AppliedAt)) {
				last = s
			}
		}
		if last == nil {
			fmt.Fprintln(out, "no applied migrations")
			return nil
		}
		m := r.byNum[last.Source.Version]
		if m.down == "" {
			return fmt.Errorf("database: migration %s has no -- prea:down section", m)
		}
		res, err := r.p.ApplyVersion(ctx, m.num, false)
		if err != nil {
			return fmt.Errorf("database: undoing %s: %w", m, err)
		}
		fmt.Fprintf(out, "undid %s (%s)\n", m, res.Duration.Round(time.Millisecond))
	}
	return nil
}

// generated renders changes as a migration file, the down in reverse
// order; ok is false when nothing would run (only commented changes).
func generated(changes []change) (sql string, ok bool) {
	var up, down []string
	for _, c := range changes {
		up = append(up, c.up+";")
		if c.down != "" {
			down = append(down, c.down+";")
		}
	}
	if !hasSQL(strings.Join(up, "\n")) {
		return "", false
	}
	slices.Reverse(down)
	sql = "-- Generated by prea from the models: review it before committing.\n\n" + strings.Join(up, "\n\n") + "\n"
	if len(down) > 0 {
		sql += "\n-- prea:down\n" + strings.Join(down, "\n") + "\n"
	}
	return sql, true
}

// generatedName names a generated migration by the tables it creates and
// alters: create_task_table, alter_task_table,
// create_list_table_and_alter_task_table. Commented-out changes don't count.
func generatedName(changes []change) string {
	var created, altered []string
	for _, c := range changes {
		if c.create && !slices.Contains(created, c.table) {
			created = append(created, c.table)
		}
	}
	for _, c := range changes {
		if hasSQL(c.up) && !slices.Contains(created, c.table) && !slices.Contains(altered, c.table) {
			altered = append(altered, c.table)
		}
	}
	var parts []string
	for _, p := range []struct {
		verb   string
		tables []string
	}{{"create", created}, {"alter", altered}} {
		if len(p.tables) == 0 {
			continue
		}
		part := p.verb + "_" + strings.Join(p.tables, "_") + "_table"
		if len(p.tables) > 1 {
			part += "s"
		}
		parts = append(parts, part)
	}
	name := strings.ToLower(strings.Join(parts, "_and_"))
	return nonName.ReplaceAllString(name, "_") // app.task: app_task
}

// writeMigration creates <dir>/<next version>_<name>.sql with content.
func writeMigration(opts Migrations, migs []migration, name, content string) (string, error) {
	version, err := nextVersion(migs, opts.Version, time.Now())
	if err != nil {
		return "", err
	}
	num, err := versionNumber(version)
	if err != nil {
		return "", err
	}
	if err := checkMigrations(append(slices.Clone(migs), migration{version: version, name: name, num: num})); err != nil {
		return "", err
	}
	if err := os.MkdirAll(opts.dir(), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(opts.dir(), version+"_"+name+".sql")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(content)
	return path, errors.Join(err, f.Close())
}

// migration is one file of the migrations dir, <version>_<name>.sql. All of
// it runs on up; what follows a "-- prea:down" line is what down runs.
//
//	-- prea:no-transaction   (optional first line: create index concurrently)
//	alter table users add column active bool not null default true;
//
//	-- prea:down
//	alter table users drop column active;
type migration struct {
	version string // as written: 0.0.2 or 20261013120000
	name    string
	num     int64 // the version as goose's integer
	up      string
	down    string
	noTx    bool
}

func (m migration) String() string { return m.version + "_" + m.name }

var (
	migrationFile = regexp.MustCompile(`^(\d+\.\d+\.\d+|\d+)_([a-z0-9_]+)\.sql$`)
	migrationName = regexp.MustCompile(`^[a-z0-9_]+$`)
	nonName       = regexp.MustCompile(`[^a-z0-9_]+`)
	downMarker    = regexp.MustCompile(`(?m)^-- prea:down[ \t]*\r?$`)
)

const noTxMarker = "-- prea:no-transaction"

// readMigrations reads the migrations of dir, ordered by version. A missing
// dir has none; files that aren't .sql are ignored.
func readMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("database: migrations: %w", err)
	}
	var migs []migration
	var errs []error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("database: migrations: %w", err)
		}
		m, err := parseMigration(e.Name(), string(content))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		migs = append(migs, m)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return migs, checkMigrations(migs)
}

func parseMigration(file, content string) (migration, error) {
	parts := migrationFile.FindStringSubmatch(file)
	if parts == nil {
		return migration{}, fmt.Errorf("database: migrations/%s: expected <version>_<name>.sql, e.g. 0.0.1_create_users.sql", file)
	}
	num, err := versionNumber(parts[1])
	if err != nil {
		return migration{}, fmt.Errorf("database: migrations/%s: %w", file, err)
	}
	m := migration{version: parts[1], name: parts[2], num: num, up: content}

	if rest, ok := strings.CutPrefix(strings.TrimLeft(m.up, " \t\r\n"), noTxMarker); ok {
		m.up, m.noTx = rest, true
	}
	if loc := downMarker.FindStringIndex(m.up); loc != nil {
		m.up, m.down = m.up[:loc[0]], m.up[loc[1]:]
		if !hasSQL(m.down) {
			return m, fmt.Errorf("database: migrations/%s: the -- prea:down section is empty", file)
		}
	}
	if !hasSQL(m.up) {
		return m, fmt.Errorf("database: migrations/%s: nothing to run", file)
	}
	return m, nil
}

// hasSQL reports whether s has a line that isn't blank or a -- comment.
func hasSQL(s string) bool {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}

// versionNumber packs a semver into an integer that sorts like it
// (major*1e12 + minor*1e6 + patch); a plain number is taken as is.
func versionNumber(v string) (int64, error) {
	var n int64
	if parts := strings.Split(v, "."); len(parts) == 3 {
		for _, p := range parts {
			i, err := strconv.ParseInt(p, 10, 64)
			if err != nil || i >= 1_000_000 {
				return 0, fmt.Errorf("version %s: each part must be below 1000000", v)
			}
			n = n*1_000_000 + i
		}
	} else {
		var err error
		if n, err = strconv.ParseInt(v, 10, 64); err != nil {
			return 0, fmt.Errorf("version %s: %w", v, err)
		}
	}
	if n < 1 {
		return 0, fmt.Errorf("version %s must be greater than zero", v)
	}
	return n, nil
}

func isSemver(version string) bool { return strings.Contains(version, ".") }

// checkMigrations sorts migs by version and rejects duplicates and a dir
// mixing semver and timestamp versions.
func checkMigrations(migs []migration) error {
	slices.SortFunc(migs, func(a, b migration) int { return cmp.Compare(a.num, b.num) })
	for i := 1; i < len(migs); i++ {
		a, b := migs[i-1], migs[i]
		switch {
		case a.num == b.num:
			return fmt.Errorf("database: migrations %s and %s have the same version", a, b)
		case isSemver(a.version) != isSemver(b.version):
			return fmt.Errorf("database: migrations %s and %s mix semver and timestamp versions", a, b)
		}
	}
	return nil
}

// nextVersion is the version of a new migration: the last semver with the
// patch bumped (0.0.1 first) or, with format timestamp, now in UTC.
func nextVersion(migs []migration, format string, now time.Time) (string, error) {
	var last string
	if len(migs) > 0 {
		last = migs[len(migs)-1].version
	}
	switch format {
	case "", "semver":
		if last == "" {
			return "0.0.1", nil
		}
		if !isSemver(last) {
			return "", fmt.Errorf("database: the migrations use timestamp versions: set database.migrations.version: timestamp")
		}
		i := strings.LastIndex(last, ".")
		patch, _ := strconv.Atoi(last[i+1:])
		return last[:i+1] + strconv.Itoa(patch+1), nil
	case "timestamp":
		if last != "" && isSemver(last) {
			return "", fmt.Errorf("database: the migrations use semver versions: set database.migrations.version: semver")
		}
		return now.UTC().Format("20060102150405"), nil
	}
	return "", fmt.Errorf("database: migrations.version %q is not semver or timestamp", format)
}

// goose writes m in goose's format. The whole up (and down) is one
// statement, so the file runs as written, $$ bodies included.
func (m migration) goose() []byte {
	var b strings.Builder
	if m.noTx {
		b.WriteString("-- +goose NO TRANSACTION\n")
	}
	b.WriteString("-- +goose Up\n-- +goose StatementBegin\n" + m.up + "\n-- +goose StatementEnd\n")
	if m.down != "" {
		b.WriteString("-- +goose Down\n-- +goose StatementBegin\n" + m.down + "\n-- +goose StatementEnd\n")
	}
	return []byte(b.String())
}
