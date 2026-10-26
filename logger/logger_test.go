package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	level, err := ParseLevel("success")
	if err != nil {
		t.Fatal(err)
	}
	Log = New(&buf, level, false)

	Debug("d")
	Info("i")
	Success("s", "id", 1)
	Error("e")

	out := buf.String()
	if strings.Contains(out, "msg=d") || strings.Contains(out, "msg=i") {
		t.Fatalf("below the level must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "level=SUCCESS msg=s id=1") || !strings.Contains(out, "level=ERROR msg=e") {
		t.Fatalf("missing lines:\n%s", out)
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Fatal("invalid level must fail")
	}
}

func TestColors(t *testing.T) {
	var buf bytes.Buffer
	Log = New(colorWriter{&buf}, LevelDebug, false)

	Error("e", "level", "x")
	Warn("w")
	Info("i")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i, want := range []string{"\x1b[31mlevel=ERROR\x1b[0m msg=e level=x", "\x1b[33mlevel=WARN\x1b[0m msg=w", "\x1b[32mlevel=INFO\x1b[0m msg=i"} {
		if !strings.HasPrefix(lines[i], "\x1b[2mtime=") || !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d: %q, want the time dimmed and ...%q", i, lines[i], want)
		}
	}
}
