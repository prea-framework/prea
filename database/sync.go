package database

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema modes: what SyncSchema does with the models' tables at startup
// (config key database.schema).
const (
	SchemaNone       = "none"        // only checks the models' tags
	SchemaValidate   = "validate"    // the tables must match the models, or the app doesn't start
	SchemaUpdate     = "update"      // creates the missing tables, columns and indexes; never drops
	SchemaCreate     = "create"      // drops the models' tables and creates them
	SchemaCreateDrop = "create-drop" // create, and drop them on shutdown
)

// repository is implemented by every component that embeds a BaseRepository.
type repository interface {
	model() (*pgxpool.Pool, reflect.Type)
}

func (r *BaseRepository[T]) model() (*pgxpool.Pool, reflect.Type) {
	return r.DB, reflect.TypeFor[T]()
}

// SyncSchema reads the models of the repositories among components and
// applies mode to their database ("" is none). Invalid tags fail in every
// mode. drop removes the tables under create-drop and does nothing otherwise;
// call it on shutdown.
func SyncSchema(ctx context.Context, mode string, components []any) (drop func(context.Context) error, err error) {
	drop = func(context.Context) error { return nil }
	mode = cmp.Or(mode, SchemaNone)
	if !slices.Contains([]string{SchemaNone, SchemaValidate, SchemaUpdate, SchemaCreate, SchemaCreateDrop}, mode) {
		return drop, fmt.Errorf("database: schema %q is not none, validate, update, create or create-drop", mode)
	}

	pools, tables, err := models(components)
	if err == nil && mode != SchemaNone && len(tables[nil]) > 0 {
		err = fmt.Errorf("database: the repository of %v has no database (DB is nil)", tables[nil][0].typ)
	}
	if err != nil || mode == SchemaNone {
		return drop, err
	}

	var errs []error
	for _, db := range pools {
		switch mode {
		case SchemaValidate:
			errs = append(errs, validate(ctx, db, tables[db]))
		case SchemaUpdate:
			errs = append(errs, exec(ctx, db, ddl(tables[db])))
		case SchemaCreate, SchemaCreateDrop:
			errs = append(errs, exec(ctx, db, slices.Concat(drops(tables[db]), ddl(tables[db]))))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return drop, err
	}
	if mode == SchemaCreateDrop {
		drop = func(ctx context.Context) error {
			var errs []error
			for _, db := range pools {
				errs = append(errs, exec(ctx, db, drops(tables[db])))
			}
			return errors.Join(errs...)
		}
	}
	return drop, nil
}

// models collects the models of the repositories among components, by
// pool and in foreign key order. Every invalid model is reported at once.
func models(components []any) (pools []*pgxpool.Pool, tables map[*pgxpool.Pool][]*table, err error) {
	tables = map[*pgxpool.Pool][]*table{}
	byName := map[string]*table{}
	var errs []error
	for _, component := range components {
		r, ok := component.(repository)
		if !ok {
			continue
		}
		db, typ := r.model()
		t, err := schemaOf(typ)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if prev, ok := byName[t.name]; ok {
			if prev.typ != typ {
				errs = append(errs, fmt.Errorf("database: table %s is mapped by %v and %v", t.name, prev.typ, typ))
			}
			continue
		}
		byName[t.name] = t
		if _, ok := tables[db]; !ok {
			pools = append(pools, db)
		}
		tables[db] = append(tables[db], t)
	}
	// A model that failed to parse has no table yet, so every foreign key to
	// it would also fail: report only the parse errors.
	if len(errs) > 0 {
		return pools, tables, errors.Join(errs...)
	}
	for _, db := range pools {
		sorted, err := ordered(tables[db])
		if err != nil {
			errs = append(errs, err)
		}
		tables[db] = sorted
	}
	return pools, tables, errors.Join(errs...)
}

// exec runs stmts in one transaction: all of them or none.
func exec(ctx context.Context, db *pgxpool.Pool, stmts []string) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("database: %s: %w", stmt, err)
			}
		}
		return nil
	})
}

// validate compares the tables with their models and reports every
// difference at once: a missing table or column, a type or a nullability.
func validate(ctx context.Context, db *pgxpool.Pool, tables []*table) error {
	changes, err := diff(ctx, db, tables)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range changes {
		if c.check {
			errs = append(errs, errors.New(c.msg))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("database: the schema doesn't match the models:\n%w", errors.Join(errs...))
	}
	return nil
}

// change is one difference between a model and its table: the statement
// that brings the table to the model, and the one that undoes it.
type change struct {
	msg    string
	up     string
	down   string // "" when there's nothing to undo
	check  bool   // validate reports it; indexes and extra columns it doesn't
	table  string // the model's table, which names the generated migration
	create bool   // creates the table
}

// diff compares every model with its table in the database.
func diff(ctx context.Context, db *pgxpool.Pool, tables []*table) ([]change, error) {
	var changes []change
	for _, t := range tables {
		cur, err := introspect(ctx, db, t)
		if err != nil {
			return nil, err
		}
		changes = append(changes, diffTable(t, cur)...)
	}
	return changes, nil
}

type dbTable struct {
	columns map[string]dbColumn
	order   []string // column names, in table order
	indexes map[string]bool
}

type dbColumn struct {
	typ      string // udt_name
	size     int    // varchar(size)
	nullable bool
}

// introspect reads the columns and index names of t's table; nil when the
// table doesn't exist.
func introspect(ctx context.Context, db *pgxpool.Pool, t *table) (*dbTable, error) {
	rows, err := db.Query(ctx, `
		select a.attname, ty.typname,
			case when ty.typname in ('varchar', 'bpchar') and a.atttypmod > 4 then a.atttypmod - 4 else 0 end,
			not a.attnotnull
		from pg_attribute a join pg_type ty on ty.oid = a.atttypid
		where a.attrelid = to_regclass($1) and a.attnum > 0 and not a.attisdropped
		order by a.attnum`, t.quoted())
	if err != nil {
		return nil, fmt.Errorf("database: reading %s: %w", t.name, err)
	}
	cur := &dbTable{columns: map[string]dbColumn{}, indexes: map[string]bool{}}
	var name string
	var col dbColumn
	if _, err := pgx.ForEachRow(rows, []any{&name, &col.typ, &col.size, &col.nullable}, func() error {
		cur.columns[name] = col
		cur.order = append(cur.order, name)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("database: reading %s: %w", t.name, err)
	}
	if len(cur.order) == 0 {
		return nil, nil
	}

	rows, err = db.Query(ctx, `
		select i.relname from pg_index x join pg_class i on i.oid = x.indexrelid
		where x.indrelid = to_regclass($1)`, t.quoted())
	if err != nil {
		return nil, fmt.Errorf("database: reading %s: %w", t.name, err)
	}
	if _, err := pgx.ForEachRow(rows, []any{&name}, func() error {
		cur.indexes[name] = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("database: reading %s: %w", t.name, err)
	}
	return cur, nil
}

// diffTable compares a model with its table (nil when it doesn't exist).
// Columns the model doesn't have are reported commented out: dropping them
// loses data, so it's the reviewer's call.
func diffTable(t *table, cur *dbTable) []change {
	tbl := t.quoted()
	var changes []change
	if cur == nil {
		if stmt := t.createSchema(); stmt != "" {
			changes = append(changes, change{up: stmt})
		}
		changes = append(changes, change{msg: "table " + t.name + " is missing", up: t.create(), down: "drop table " + tbl, check: true, create: true})
		cur = &dbTable{columns: map[string]dbColumn{}, indexes: map[string]bool{}}
		for _, c := range t.columns {
			cur.columns[c.name] = dbColumn{c.sqlType, c.size, c.nullable}
		}
	}

	for _, c := range t.columns {
		got, ok := cur.columns[c.name]
		col := quote(c.name)
		alter := "alter table " + tbl + " alter column " + col
		switch {
		case !ok:
			changes = append(changes, change{
				msg: t.name + "." + c.name + " is missing",
				up:  "alter table " + tbl + " add column " + c.definition(), down: "alter table " + tbl + " drop column " + col, check: true,
			})
		case got.typ != c.sqlType || got.size != c.size:
			want, was := typeName(c.sqlType, c.size), typeName(got.typ, got.size)
			changes = append(changes, change{
				msg: fmt.Sprintf("%s.%s is %s, the model wants %s", t.name, c.name, was, want),
				up:  alter + " type " + want + " using " + col + "::" + want, down: alter + " type " + was + " using " + col + "::" + was, check: true,
			})
		case got.nullable && !c.nullable:
			changes = append(changes, change{
				msg: t.name + "." + c.name + " is nullable: make the field a pointer, or the column not null",
				up:  alter + " set not null", down: alter + " drop not null", check: true,
			})
		case !got.nullable && c.nullable:
			changes = append(changes, change{
				msg: t.name + "." + c.name + " is not null: make the field a value, or the column nullable",
				up:  alter + " drop not null", down: alter + " set not null", check: true,
			})
		}
	}
	for _, name := range cur.order {
		if !slices.ContainsFunc(t.columns, func(c column) bool { return c.name == name }) {
			changes = append(changes, change{
				msg: t.name + "." + name + " is not in the model",
				up:  "-- " + t.name + "." + name + " is not in the model; to drop it (its data is lost):\n-- alter table " + tbl + " drop column " + quote(name),
			})
		}
	}
	schema := t.name[:strings.LastIndex(t.name, ".")+1] // "app." or ""
	for _, i := range t.indexes() {
		if !cur.indexes[i.name] {
			changes = append(changes, change{msg: "index " + i.name + " is missing", up: i.sql, down: "drop index " + quoteTable(schema+i.name)})
		}
	}
	for i := range changes {
		changes[i].table = t.name
	}
	return changes
}

func typeName(typ string, size int) string {
	if size > 0 {
		return typ + "(" + strconv.Itoa(size) + ")"
	}
	return typ
}
