package main

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain lets testscript invoke the redrill CLI as the `redrill` command
// inside scripts (re-execing this test binary), while ordinary go tests still
// run via m.Run().
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"redrill": func() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) },
	})
}

// TestScripts runs the CLI / exit-code / config sessions in testdata/script as
// readable txtar command files (no shell harness).
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata/script"})
}
