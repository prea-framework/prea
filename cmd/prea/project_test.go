package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The generated code is the product: create projects with prea new, generate
// preás and middlewares in them, then vet, test (prea tests) and build them.
// Offline: the framework comes from this clone (--local) and every other
// module from the module cache.
func TestGeneratedProject(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles projects")
	}
	framework, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"GOFLAGS": "-mod=mod", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off"} {
		t.Setenv(k, v)
	}
	gocmd := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	prea := func(args ...string) {
		t.Helper()
		if err := run(args...); err != nil {
			t.Fatalf("prea %s: %v", strings.Join(args, " "), err)
		}
	}

	// -race needs cgo, and a machine without a C compiler (often Windows) has none.
	testArgs := []string{"-count=1"}
	if out, _ := exec.Command("go", "env", "CGO_ENABLED").Output(); strings.TrimSpace(string(out)) == "1" {
		testArgs = append(testArgs, "-race")
	}

	t.Run("minimal", func(t *testing.T) {
		t.Chdir(t.TempDir())
		prea("new", "shop", "--module", "example.com/shop", "--local", framework)
		t.Chdir("shop")
		for file, want := range map[string]string{"go.mod": "module example.com/shop", "main.go": `"example.com/shop/preas"`} {
			if b, _ := os.ReadFile(file); !strings.Contains(string(b), want) {
				t.Fatalf("--module must reach %s: %s", file, b)
			}
		}
		prea("g", "m", "rate_limit")
		prea("g", "m", "request_timing", "--func")
		gocmd("vet", "./...")
		prea(append([]string{"tests"}, testArgs...)...) // the template's tests: the health preá and tests/
		prea("build")
		serves(t, exe(filepath.Join("bin", "shop")))
	})

	t.Run("with-db", func(t *testing.T) {
		t.Chdir(t.TempDir())
		prea("new", "shop", "--with-db", "--local", framework)
		t.Chdir("shop")
		prea("g", "p", "order_item")
		prea("g", "p", "Product")
		prea("g", "m", "rate_limit")
		prea("g", "m", "request_timing", "--func")
		write(t, filepath.Join("tests", "crud_test.go"), crudTest)
		gocmd("vet", "./...")

		url := os.Getenv("PREA_TEST_DATABASE_URL")
		if url == "" {
			t.Skip("PREA_TEST_DATABASE_URL not set: generated, vetted, not run")
		}
		t.Setenv("TEST_DATABASE_URL", url) // config/application-tests.yaml
		prea(append([]string{"tests"}, testArgs...)...)
	})

	t.Run("errors", func(t *testing.T) {
		t.Chdir(t.TempDir())
		write(t, filepath.Join("taken", "notes.txt"), "mine")
		if err := run("new", "taken", "--local", framework); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("a folder with files must be refused: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join("taken", "notes.txt")); string(b) != "mine" {
			t.Fatal("a refused prea new must not touch the folder")
		}
		for _, name := range []string{"9shop", "my shop", "../shop"} {
			if err := run("new", name, "--local", framework); err == nil || !strings.Contains(err.Error(), "invalid name") {
				t.Errorf("%q: %v", name, err)
			}
		}
		if err := run("new", "shop", "--local", t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a clone") {
			t.Fatalf("--local must point at the framework: %v", err)
		}
		if _, err := os.Stat("shop"); !os.IsNotExist(err) {
			t.Fatal("a failed prea new must leave nothing behind")
		}
	})
}

// serves runs the built binary on a free port and expects GET /health to
// answer 200, then stops it with SIGINT.
func serves(t *testing.T, bin string) {
	t.Helper()
	port := usePort(t)
	server := exec.Command(bin)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Wait()
	defer func() {
		if err := server.Process.Signal(os.Interrupt); err != nil {
			server.Process.Kill() // Windows cannot send an interrupt: Wait would never return
		}
	}()
	for range 50 {
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /health: %d", res.StatusCode)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never answered on port %d", bin, port)
}

// usePort sets server.port of the project in the current dir to a free port.
func usePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	yaml, _ := os.ReadFile(filepath.Join("config", "application.yaml"))
	write(t, filepath.Join("config", "application.yaml"), strings.Replace(string(yaml), "port: 8000", fmt.Sprintf("port: %d", port), 1))
	return port
}

// crudTest goes through the generated preá's CRUD over HTTP, with the whole
// app and the generated middlewares.
const crudTest = `package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/prea-framework/prea"

	"shop/middleware"
	"shop/preas"

	"github.com/gin-gonic/gin"
)

func TestCRUD(t *testing.T) {
	engine := prea.Test(t, prea.Config{
		Modules:     preas.Modules,
		Middlewares: append(slices.Clone(preas.Middlewares), middleware.RateLimit{}, middleware.RequestTiming),
		Security:    preas.Security,
	}).(*gin.Engine)

	var routes []string
	for _, r := range engine.Routes() {
		routes = append(routes, r.Method+" "+r.Path)
	}
	for _, base := range []string{"/order-item", "/product", "/item"} {
		for _, want := range []string{"GET " + base, "GET " + base + "/:id", "POST " + base, "PATCH " + base + "/:id", "DELETE " + base + "/:id"} {
			if !slices.Contains(routes, want) {
				t.Errorf("missing route %s in %v", want, routes)
			}
		}
	}

	type reply struct {
		Code int
		Data struct{ ID, Name string }
	}
	call := func(method, path, body string, status int) reply {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		var out reply
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	created := call("POST", "/order-item", ` + "`" + `{"name": "  caneta  "}` + "`" + `, http.StatusCreated)
	if created.Data.ID == "" || created.Data.Name != "caneta" {
		t.Fatalf("create: %+v", created)
	}
	call("POST", "/order-item", ` + "`" + `{"name": ""}` + "`" + `, http.StatusBadRequest)
	call("POST", "/order-item", ` + "`" + `{"name": "   "}` + "`" + `, http.StatusBadRequest)
	if got := call("GET", "/order-item/"+created.Data.ID, "", http.StatusOK); got.Data.Name != "caneta" {
		t.Fatalf("get: %+v", got)
	}
	if got := call("PATCH", "/order-item/"+created.Data.ID, ` + "`" + `{"name": "lápis"}` + "`" + `, http.StatusOK); got.Data.Name != "lápis" {
		t.Fatalf("update: %+v", got)
	}
	call("GET", "/order-item", "", http.StatusOK)
	call("DELETE", "/order-item/"+created.Data.ID, "", http.StatusNoContent)
	call("GET", "/order-item/"+created.Data.ID, "", http.StatusNotFound)
	call("GET", "/order-item/not-a-uuid", "", http.StatusBadRequest)
}
`
