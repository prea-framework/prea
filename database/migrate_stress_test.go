package database

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Heavier tests of the migrations: small pools, concurrency, failures,
// timeouts and many files. They need PREA_TEST_DATABASE_URL, like the rest.

// poolOf opens a second pool on the test database with at most n connections.
func poolOf(t *testing.T, n int) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("PREA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = int32(n)
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// noLeak fails when a command left a connection out of the pool: a leaked
// one would keep holding the migrations lock. A connection closed by a
// canceled context is destroyed in the background, so it waits a little.
func noLeak(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for range 100 {
		if pool.Stat().AcquiredConns() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d connections still acquired", pool.Stat().AcquiredConns())
}

// With one connection, generating from the models must not wait for a
// second one while the first holds the lock.
func TestMigrationsPoolOfOne(t *testing.T) {
	m := newMigrator(t, "notes")
	pool := poolOf(t, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opts := Options{Schema: SchemaValidate, Migrations: Migrations{Dir: m.opts.Dir, FromModels: true, OnStart: true}}
	if _, err := Setup(pool, opts)(ctx, []any{&BaseRepository[note]{DB: pool}}); err != nil {
		t.Fatalf("startup with a pool of one: %v", err)
	}
	if got := m.rows(); len(got) != 1 || !strings.HasPrefix(got[0], "0.0.1 create_notes_table") {
		t.Fatalf("rows: %v", got)
	}
	noLeak(t, pool)
}

// Ten instances starting at once on twenty migrations: each runs once, none
// fails, and every connection goes back to the pool.
func TestMigrationsConcurrentStartup(t *testing.T) {
	var tables []string
	for i := 1; i <= 20; i++ {
		tables = append(tables, fmt.Sprintf("mig_s%d", i))
	}
	m := newMigrator(t, strings.Join(tables, ", "))
	pool := poolOf(t, 30)
	for i := 1; i <= 20; i++ {
		m.write(fmt.Sprintf("0.0.%d_t%d.sql", i, i), fmt.Sprintf("create table mig_s%d (id int);\nselect pg_sleep(0.01);\n-- prea:down\ndrop table mig_s%d;", i, i))
	}
	t.Cleanup(func() { m.run("down", "20") })
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, err := Setup(pool, Options{Migrations: Migrations{Dir: m.opts.Dir, OnStart: true}})(t.Context(), nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := m.rows(); len(got) != 20 || got[0] != "0.0.1 t1" || got[19] != "0.0.20 t20" {
		t.Fatalf("rows: %v", got)
	}
	noLeak(t, pool)
}

// Eight instances find goose's table at once: one converts it, the others
// read the result.
func TestMigrationsConcurrentConversion(t *testing.T) {
	m := newMigrator(t, "")
	pool := poolOf(t, 20)
	if _, err := m.pool.Exec(t.Context(), gooseTable+`insert into prea_migrations (version_id, is_applied) values (0, true), (1, true), (2, true)`); err != nil {
		t.Fatal(err)
	}
	m.write("0.0.1_a.sql", "select 1;")
	m.write("0.0.2_b.sql", "select 2;")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- Command(t.Context(), pool, m.opts, []string{"status"}, io.Discard) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 a", "0.0.2 b"}) {
		t.Fatalf("rows: %v", got)
	}
	noLeak(t, pool)
}

// goose's CLI wrote a row per up and down: only a version's last row counts,
// and a version applied twice is one row.
func TestMigrationsFromGooseHistory(t *testing.T) {
	m := newMigrator(t, "")
	if _, err := m.pool.Exec(t.Context(), gooseTable+`insert into prea_migrations (version_id, is_applied) values
		(0, true), (1, true), (2, true), (2, false), (3, true), (3, false), (3, true)`); err != nil {
		t.Fatal(err)
	}
	m.write("0.0.1_a.sql", "select 1;")
	m.write("0.0.2_b.sql", "select 2;")
	m.write("0.0.3_c.sql", "select 3;")
	// 0.0.2 was undone, so it's pending below 0.0.3: forward only stops it
	m.mustFail([]string{"0.0.2_b: pending, but older than the last applied migration, 0.0.3_c"}, "status")
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 a", "0.0.3 c"}) {
		t.Fatalf("rows: %v", got)
	}
}

// A failing migration leaves nothing behind, and the ones after it don't
// run; a failing down keeps the migration applied.
func TestMigrationsFailures(t *testing.T) {
	m := newMigrator(t, "mig_f1, mig_f2, mig_f3")
	m.write("0.0.1_f1.sql", "create table mig_f1 (id int);\n-- prea:down\ndrop table mig_f1;")
	m.write("0.0.2_f2.sql", "create table mig_f2 (id int);\ninsert into mig_f2 values (1);\nselect 1/0;\n-- prea:down\ndrop table mig_f2;")
	m.write("0.0.3_f3.sql", "create table mig_f3 (id int);\n-- prea:down\ndrop table mig_f3;")
	out, err := m.run("up")
	if err == nil || !strings.Contains(err.Error(), "migration 0.0.2_f2") || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("up: %v", err)
	}
	if out != "" && (!strings.HasPrefix(out, "applied 0.0.1_f1") || strings.Contains(out, "0.0.3")) {
		t.Fatalf("output: %q", out)
	}
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 f1"}) {
		t.Fatalf("rows: %v", got)
	}
	if m.exists("mig_f2") || m.exists("mig_f3") {
		t.Fatal("the failed migration and the next one left tables")
	}

	m.write("0.0.2_f2.sql", "create table mig_f2 (id int);\n-- prea:down\ndrop table mig_f2;\nselect 1/0;")
	m.mustRun("up")
	out, err = m.run("down", "2") // 0.0.3 is undone, then 0.0.2's down fails
	if err == nil || !strings.Contains(err.Error(), "undoing 0.0.2_f2") || !strings.Contains(out, "undid 0.0.3_f3") {
		t.Fatalf("down 2: %q, %v", out, err)
	}
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 f1", "0.0.2 f2"}) || !m.exists("mig_f2") {
		t.Fatalf("a failed down keeps the migration: %v", got)
	}
	noLeak(t, m.pool)
}

// Outside a transaction, a failed migration isn't recorded either; Postgres
// runs a file's statements in one implicit transaction, so a no-transaction
// file holds a single concurrent statement.
func TestMigrationsNoTransactionFailures(t *testing.T) {
	m := newMigrator(t, "mig_n")
	m.write("0.0.1_n.sql", "create table mig_n (a int, b int);")
	m.write("0.0.2_ia.sql", "-- prea:no-transaction\ncreate index concurrently mig_n_a on mig_missing (a);")
	m.mustFail([]string{"migration 0.0.2_ia", "mig_missing"}, "up")
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 n"}) {
		t.Fatalf("rows: %v", got)
	}
	m.write("0.0.2_ia.sql", "-- prea:no-transaction\ncreate index concurrently mig_n_a on mig_n (a);\ncreate index concurrently mig_n_b on mig_n (b);")
	m.mustFail([]string{"migration 0.0.2_ia", "cannot run inside a transaction block"}, "up")
	if got := m.rows(); !slices.Equal(got, []string{"0.0.1 n"}) {
		t.Fatalf("rows: %v", got)
	}
	noLeak(t, m.pool)
}

// Versions compare as numbers: 0.0.10 after 0.0.9, 0.1.0 after 0.0.999.
func TestMigrationsVersionOrder(t *testing.T) {
	m := newMigrator(t, "")
	versions := []string{"0.0.2", "0.0.9", "0.0.10", "0.0.999", "0.1.0", "1.0.0", "10.0.0"}
	for _, v := range slices.Backward(versions) { // written out of order
		m.write(v+"_v.sql", "select 1;\n-- prea:down\nselect 1;")
	}
	m.mustRun("up")
	var want []string
	for _, v := range versions {
		want = append(want, v+" v")
	}
	if got := m.rows(); !slices.Equal(got, want) {
		t.Fatalf("applied in order: %v", got)
	}
	out := m.mustRun("down", "3")
	if !strings.HasPrefix(out, "undid 10.0.0_v") || !strings.Contains(out, "undid 1.0.0_v") || !strings.Contains(out, "undid 0.1.0_v") || strings.Contains(out, "0.0.999") {
		t.Fatalf("down from the highest: %q", out)
	}
}

// Another session holding the lock makes a command wait; its context ends
// the wait, and nothing stays locked or acquired.
func TestMigrationsLockWait(t *testing.T) {
	m := newMigrator(t, "")
	m.write("0.0.1_a.sql", "select 1;")
	holder, err := m.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Release) // before the pool closes, even if the test fails
	if _, err := holder.Exec(t.Context(), migrationsLock); err != nil {
		t.Fatal(err)
	}
	pool := poolOf(t, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Command(ctx, pool, m.opts, []string{"up"}, io.Discard); err == nil {
		t.Fatal("up must wait for the lock and stop with the context")
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waited %s", d)
	}
	noLeak(t, pool)

	holder.Exec(t.Context(), "select pg_advisory_unlock_all()")
	if err := Command(t.Context(), pool, m.opts, []string{"up"}, io.Discard); err != nil {
		t.Fatalf("after the lock is free: %v", err)
	}
	noLeak(t, pool)
}

// Every command, failing or not, gives its connection back.
func TestMigrationsConnectionsReleased(t *testing.T) {
	m := newMigrator(t, "mig_r")
	pool := poolOf(t, 1) // a leak would make the next command wait forever
	m.pool = pool
	m.write("0.0.1_r.sql", "create table mig_r (id int);\n-- prea:down\ndrop table mig_r;")
	m.write("0.0.2_bad.sql", "select 1/0;")
	for _, args := range [][]string{{"status"}, {"up"}, {"up", "0.0.1"}, {"down"}, {"up", "9.9.9"}, {"create", "x"}, {"validate"}} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		Command(ctx, pool, m.opts, args, io.Discard)
		cancel()
		noLeak(t, pool)
	}
	m.remove("0.0.1_r.sql") // a failing check
	Command(t.Context(), pool, m.opts, []string{"status"}, io.Discard)
	noLeak(t, pool)
}

// Hundreds of migrations: applied in order, listed, and undone, quickly.
func TestMigrationsMany(t *testing.T) {
	m := newMigrator(t, "")
	const n = 300
	for i := 1; i <= n; i++ {
		m.write(fmt.Sprintf("0.0.%d_m.sql", i), fmt.Sprintf("select %d;\n-- prea:down\nselect %d;", i, i))
	}
	start := time.Now()
	if out := m.mustRun("up"); strings.Count(out, "applied") != n {
		t.Fatalf("applied %d", strings.Count(out, "applied"))
	}
	if out := m.mustRun("status"); strings.Count(out, "applied") != n {
		t.Fatalf("status: %d applied", strings.Count(out, "applied"))
	}
	if out := m.mustRun("down", strconv.Itoa(n)); strings.Count(out, "undid") != n {
		t.Fatalf("undid %d", strings.Count(out, "undid"))
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("%d migrations took %s", n, d)
	}
}

// The checksum ignores what doesn't change the SQL that runs, and catches
// what does.
func TestMigrationChecksum(t *testing.T) {
	base := migration{up: "create table a (\n\tid int\n);"}.checksum()
	for _, up := range []string{
		"create table a (\r\n\tid int\r\n);",
		"\n\ncreate table a (\n\tid int\n);\n\n",
		"  create table a (\n\tid int\n);\t",
	} {
		if (migration{up: up}).checksum() != base {
			t.Errorf("%q must have the same checksum", up)
		}
	}
	for _, up := range []string{
		"create table a (\n\tid bigint\n);",
		"create table a (\n  id int\n);",
		"create table a (\n\tid int\n); -- a comment",
		"CREATE TABLE a (\n\tid int\n);",
	} {
		if (migration{up: up}).checksum() == base {
			t.Errorf("%q must have another checksum", up)
		}
	}
}

func FuzzParseMigration(f *testing.F) {
	f.Add("0.0.1_a.sql", "create table a (id int);\n-- prea:down\ndrop table a;")
	f.Add("20261013120000_b.sql", "-- prea:no-transaction\ncreate index concurrently i on a (b);")
	f.Add("0.0.2_c.sql", "create function f() returns int as $$ select 1; $$ language sql;\r\n-- prea:down\r\ndrop function f;\r\n")
	f.Fuzz(func(t *testing.T, file, content string) {
		m, err := parseMigration(file, content)
		if err != nil {
			return
		}
		if !hasSQL(m.up) || m.num < 1 || m.String()+".sql" != file {
			t.Fatalf("%q: %+v", file, m)
		}
		if strings.Contains(content, "\r") {
			return // a checkout turns \n into \r\n, never \r\n into \r\r\n
		}
		again, _ := parseMigration(file, strings.ReplaceAll(content, "\n", "\r\n"))
		if again.checksum() != m.checksum() {
			t.Fatalf("%q: CRLF changed the checksum", file)
		}
	})
}
