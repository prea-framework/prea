package di

import (
	"strings"
	"testing"
)

func TestUnwrap(t *testing.T) {
	db := &DB{Name: "pg"}
	tests := map[string]any{
		"plain":           db,
		"exported":        Export(db),
		"named":           Named("a", db),
		"named, exported": Named("a", Export(db)),
		"exported, named": Export(Named("a", db)),
	}
	for name, provider := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Unwrap(provider); got != db {
				t.Fatalf("Unwrap() = %v, want the provider itself", got)
			}
		})
	}
}

// settings is a Values source that serves a fixed set of keys.
type settings map[string]string

func (s settings) Value(key string, dst any) error {
	if v, ok := s[key]; ok {
		*dst.(*string) = v
	}
	return nil
}

func TestFillValue(t *testing.T) {
	type Config struct {
		Name  string `value:"app.name"`
		Owner string `value:"app.owner"`
		Kept  string `value:"app.name"`
	}

	c := New(settings{"app.name": "prea"})
	module := &Module{Name: "m", Providers: []any{Config{Kept: "manual"}}}
	if err := c.Load(module); err != nil {
		t.Fatal(err)
	}
	got := c.Providers(module)[0].(*Config)
	if got.Name != "prea" {
		t.Fatalf("Name = %q, want the configured value", got.Name)
	}
	if got.Owner != "" {
		t.Fatalf("Owner = %q, a missing key must leave the field as it is", got.Owner)
	}
	if got.Kept != "manual" {
		t.Fatalf("Kept = %q, a field already set must not be overwritten", got.Kept)
	}
}

func TestFillValueErrors(t *testing.T) {
	type NoKey struct {
		Name string `value:""`
	}
	type Unexported struct {
		name string `value:"app.name"`
	}
	type Plain struct {
		Name string `value:"app.name"`
	}

	tests := map[string]struct {
		container *Container
		provider  any
		want      string
	}{
		"empty key":       {New(settings{}), NoKey{}, "value tag needs a key"},
		"unexported":      {New(settings{}), Unexported{}, "must be exported to receive a value"},
		"no value source": {New(), Plain{}, "needs a config"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := tt.container.Load(&Module{Name: "m", Providers: []any{tt.provider}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
