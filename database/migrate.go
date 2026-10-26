package database

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prea-framework/prea/logger"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
// Both migration steps first check the applied migrations against their
// files (see runner.check), and stop the app when they don't match.
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
// generatedName, with what the database lacks to match the models. With
// migrations pending it does nothing: they may already have these changes.
func generateMigration(ctx context.Context, db *pgxpool.Pool, m Migrations, components []any) error {
	migs, err := readMigrations(m.dir())
	if err != nil {
		return err
	}
	r, err := openRunner(ctx, db, m, migs)
	if err != nil {
		return err
	}
	defer r.close()
	if len(r.pending()) > 0 {
		logger.Info("database: migrations are pending: apply them before generating one from the models")
		return nil
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
//	down [n]        undoes the n highest applied versions (default 1)
//	status          lists every migration: applied or pending
//	validate        checks the files without touching the database
//
// With a database, every command but validate first checks the applied
// migrations against their files (see runner.check).
func Command(ctx context.Context, db *pgxpool.Pool, opts Migrations, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("database: migrate needs a command: create, up, down, status or validate")
	}
	migs, err := readMigrations(opts.dir())
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	n := 1 // for down
	switch cmd {
	case "validate":
		fmt.Fprintf(out, "migrations ok: %d\n", len(migs))
		return nil
	case "create":
		if len(args) != 1 || !migrationName.MatchString(args[0]) {
			return errors.New("database: create needs a snake_case name, e.g. create add_users_active")
		}
	case "up":
		if len(args) > 1 {
			return errors.New("database: up takes at most a version")
		}
	case "down":
		if len(args) > 0 {
			if n, err = strconv.Atoi(args[0]); err != nil || n < 1 || len(args) > 1 {
				return errors.New("database: down takes the number of migrations to undo, e.g. down 2")
			}
		}
	case "status":
	default:
		return fmt.Errorf("database: unknown migrate command %q", cmd)
	}
	var r *runner
	if db != nil {
		if r, err = openRunner(ctx, db, opts, migs); err != nil {
			return err
		}
		defer r.close()
	}
	switch {
	case cmd == "create":
		path, err := writeMigration(opts, migs, args[0], "-- The SQL to run. Below a \"-- prea:down\" line, the SQL that undoes it.\n")
		if err == nil {
			fmt.Fprintln(out, "created", path)
		}
		return err
	case len(migs) == 0:
		fmt.Fprintf(out, "no migrations in %s\n", opts.dir())
		return nil
	case r == nil:
		return errNoDatabase
	case cmd == "up":
		return r.up(ctx, args, out)
	case cmd == "down":
		return r.down(ctx, n, out)
	}
	r.status(out)
	return nil
}

// The applied migrations, one row each: the version as written in the file
// name ("0.0.1" or "20261013120000"), the name, and the checksum of the up
// that ran. A migration that is undone loses its row.
const (
	migrationsTable    = "prea_migrations"
	migrationsTableDDL = `create table if not exists ` + migrationsTable + ` (
	version    text primary key,
	name       text not null,
	checksum   text not null,
	applied_at timestamptz not null default clock_timestamp()
)`
	migrationsLock = "select pg_advisory_lock(hashtext('" + migrationsTable + "'))"
)

type appliedMigration struct {
	Version   string
	Name      string
	Checksum  string
	AppliedAt time.Time
}

func (a appliedMigration) String() string { return strings.TrimSuffix(a.Version+"_"+a.Name, "_") }

// runner runs the migrations on one connection that holds an advisory lock
// until close, so two instances never migrate at the same time.
type runner struct {
	conn    *pgxpool.Conn
	migs    []migration // ordered by version
	byNum   map[int64]migration
	applied map[int64]appliedMigration
}

// openRunner takes the lock, creates prea_migrations (or converts the one
// goose kept, up to Preá v0.0.4), reads the applied migrations and checks them.
func openRunner(ctx context.Context, db *pgxpool.Pool, opts Migrations, migs []migration) (*runner, error) {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("database: migrations: %w", err)
	}
	r := &runner{conn: conn, migs: migs, byNum: map[int64]migration{}, applied: map[int64]appliedMigration{}}
	for _, m := range migs {
		r.byNum[m.num] = m
	}
	if _, err := conn.Exec(ctx, migrationsLock); err != nil {
		conn.Release()
		return nil, fmt.Errorf("database: migrations: %w", err)
	}
	if err := r.load(ctx, opts); err != nil {
		r.close()
		return nil, err
	}
	if err := r.check(opts); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (r *runner) close() {
	ctx := context.Background()
	if _, err := r.conn.Exec(ctx, "select pg_advisory_unlock(hashtext('"+migrationsTable+"'))"); err != nil {
		r.conn.Conn().Close(ctx) // the session ends, and the lock with it
	}
	r.conn.Release()
}

func (r *runner) load(ctx context.Context, opts Migrations) error {
	err := pgx.BeginFunc(ctx, r.conn, func(tx pgx.Tx) error {
		var goose bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from information_schema.columns
			where table_schema = current_schema() and table_name = $1 and column_name = 'version_id')`, migrationsTable).Scan(&goose); err != nil {
			return err
		}
		if !goose {
			_, err := tx.Exec(ctx, migrationsTableDDL)
			return err
		}
		// goose kept only a number per version: 0.0.2 was 2. Take the version
		// and name from the file; one without a file stays as the number, and
		// check reports it.
		rows, _ := tx.Query(ctx, "select version_id, tstamp::timestamptz from "+migrationsTable+" where version_id > 0 and is_applied order by id")
		type gooseRow struct {
			Num int64
			At  time.Time
		}
		old, err := pgx.CollectRows(rows, pgx.RowToStructByPos[gooseRow])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "drop table "+migrationsTable+"; "+migrationsTableDDL); err != nil {
			return err
		}
		for _, o := range old {
			a := appliedMigration{Version: versionString(o.Num, opts.Version), AppliedAt: o.At}
			if m, ok := r.byNum[o.Num]; ok {
				a = appliedMigration{m.version, m.name, m.checksum(), o.At}
			}
			if _, err := tx.Exec(ctx, "insert into "+migrationsTable+" values ($1, $2, $3, $4)", a.Version, a.Name, a.Checksum, a.AppliedAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("database: migrations: preparing %s: %w", migrationsTable, err)
	}

	rows, _ := r.conn.Query(ctx, "select version, name, checksum, applied_at from "+migrationsTable)
	applied, err := pgx.CollectRows(rows, pgx.RowToStructByPos[appliedMigration])
	if err != nil {
		return fmt.Errorf("database: migrations: reading %s: %w", migrationsTable, err)
	}
	for _, a := range applied {
		num, err := versionNumber(a.Version)
		if err != nil {
			return fmt.Errorf("database: migrations: %s has %q: %w", migrationsTable, a.Version, err)
		}
		r.applied[num] = a
	}
	return nil
}

// check fails when an applied migration has no file, a file with another
// name, or a file whose up changed since it ran: deleting, renaming or
// replacing a migration doesn't undo it, and a new file that takes its
// version would never run. It also fails when a pending migration is older
// than the last applied one: migrations only run forward.
func (r *runner) check(opts Migrations) error {
	var changed, older []string
	nums := slices.Sorted(maps.Keys(r.applied))
	for _, num := range nums {
		a := r.applied[num]
		m, ok := r.byNum[num]
		switch {
		case !ok:
			changed = append(changed, a.String()+": applied, but its file is missing")
		case m.name != a.Name:
			changed = append(changed, a.String()+": applied, but its version is now "+m.String()+".sql")
		case m.checksum() != a.Checksum:
			changed = append(changed, a.String()+": applied, but its SQL changed since")
		}
	}
	var last appliedMigration // the highest version applied
	if len(nums) > 0 {
		last = r.applied[nums[len(nums)-1]]
	}
	for _, m := range r.pending() {
		if len(nums) > 0 && m.num < nums[len(nums)-1] {
			older = append(older, m.String()+": pending, but older than the last applied migration, "+last.String())
		}
	}
	if len(changed)+len(older) == 0 {
		return nil
	}
	msg := fmt.Sprintf("database: the migrations in %s/ don't match the database:\n  %s", opts.dir(), strings.Join(append(changed, older...), "\n  "))
	if len(changed) > 0 {
		msg += "\nDeleting, renaming, editing or replacing an applied migration doesn't undo it. Restore the file (e.g. git checkout); " +
			"to undo the migration, run prea migrate down, then delete the file"
	}
	if len(older) > 0 {
		msg += "\nMigrations only run forward: give the pending one a version above " + last.Version
	}
	return errors.New(msg)
}

func (r *runner) pending() []migration {
	return slices.DeleteFunc(slices.Clone(r.migs), func(m migration) bool {
		_, ok := r.applied[m.num]
		return ok
	})
}

// up applies the pending migrations in version order, or those up to a
// version; check made sure they all come after the applied ones.
func (r *runner) up(ctx context.Context, to []string, out io.Writer) error {
	last := int64(math.MaxInt64)
	if len(to) > 0 {
		num, err := versionNumber(to[0])
		if _, ok := r.byNum[num]; err != nil || !ok {
			return fmt.Errorf("database: no migration with version %s", to[0])
		}
		last = num
	}
	n := 0
	for _, m := range r.pending() {
		if m.num > last {
			break
		}
		start := time.Now()
		a := appliedMigration{m.version, m.name, m.checksum(), start}
		if err := r.run(ctx, m, m.up, "insert into "+migrationsTable+" (version, name, checksum) values ($1, $2, $3)", a.Version, a.Name, a.Checksum); err != nil {
			return fmt.Errorf("database: migration %s: %w", m, err)
		}
		r.applied[m.num] = a
		fmt.Fprintf(out, "applied %s (%s)\n", m, time.Since(start).Round(time.Millisecond))
		n++
	}
	if n == 0 {
		fmt.Fprintln(out, "no pending migrations")
	}
	return nil
}

// down undoes the applied migrations from the highest version, one by one,
// so the ones left pending are always above the applied ones; it stops
// before one without a -- prea:down section.
func (r *runner) down(ctx context.Context, n int, out io.Writer) error {
	nums := slices.Sorted(maps.Keys(r.applied))
	slices.Reverse(nums)
	if len(nums) == 0 {
		fmt.Fprintln(out, "no applied migrations")
		return nil
	}
	for _, num := range nums[:min(n, len(nums))] {
		m := r.byNum[num] // check made sure it's there
		if m.down == "" {
			return fmt.Errorf("database: migration %s has no -- prea:down section", m)
		}
		start := time.Now()
		if err := r.run(ctx, m, m.down, "delete from "+migrationsTable+" where version = $1", r.applied[num].Version); err != nil {
			return fmt.Errorf("database: undoing %s: %w", m, err)
		}
		delete(r.applied, num)
		fmt.Fprintf(out, "undid %s (%s)\n", m, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// run executes sql as written, in one round trip so $$ bodies and several
// statements work, and records it with record; both in one transaction
// unless m is -- prea:no-transaction.
func (r *runner) run(ctx context.Context, m migration, sql, record string, args ...any) error {
	if m.noTx {
		if _, err := r.conn.Exec(ctx, sql); err != nil {
			return err
		}
		_, err := r.conn.Exec(ctx, record, args...)
		return err
	}
	return pgx.BeginFunc(ctx, r.conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql); err != nil { // no args: the simple protocol
			return err
		}
		_, err := tx.Exec(ctx, record, args...)
		return err
	})
}

func (r *runner) status(out io.Writer) {
	for _, m := range r.migs {
		state := "pending"
		if a, ok := r.applied[m.num]; ok {
			state = "applied " + a.AppliedAt.Local().Format(time.DateTime)
		}
		fmt.Fprintf(out, "%-40s %s\n", m, state)
	}
}

// checksum is the checksum of what m runs on up, the same with \r\n line
// endings or blank lines around it. The down isn't in it: it can still
// change, e.g. to add one so an applied migration can be undone.
func (m migration) checksum() string {
	h := sha256.Sum256([]byte(strings.TrimSpace(strings.ReplaceAll(m.up, "\r", ""))))
	return hex.EncodeToString(h[:])
}

// versionString unpacks versionNumber.
func versionString(num int64, format string) string {
	if format == "timestamp" {
		return strconv.FormatInt(num, 10)
	}
	return fmt.Sprintf("%d.%d.%d", num/1e12, num/1e6%1e6, num%1e6)
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
	num     int64 // the version as an integer that sorts like it
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
