package exec

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/driver/borg"
)

func TestDecorate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		policy   IOPolicy
		wantName string
		wantArgs []string
	}{
		{"none", IOPolicy{}, "borg", []string{"list", "/r"}},
		{"nice only", IOPolicy{NiceCPU: 10}, "nice", []string{"-n", "10", "borg", "list", "/r"}},
		{"ionice idle", IOPolicy{IOClass: "idle"}, "ionice", []string{"-c", "3", "borg", "list", "/r"}},
		{"ionice best-effort", IOPolicy{IOClass: "best-effort"}, "ionice", []string{"-c", "2", "borg", "list", "/r"}},
		{"ionice none is inert", IOPolicy{IOClass: "none"}, "borg", []string{"list", "/r"}},
		{"both", IOPolicy{NiceCPU: 5, IOClass: "idle"}, "nice", []string{"-n", "5", "ionice", "-c", "3", "borg", "list", "/r"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotArgs := tt.policy.decorate("borg", []string{"list", "/r"})
			if gotName != tt.wantName || !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("decorate = %q %v, want %q %v", gotName, gotArgs, tt.wantName, tt.wantArgs)
			}
		})
	}
}

func TestWrapIOPolicyInertReturnsBase(t *testing.T) {
	t.Parallel()
	var gotName string
	var gotArgs []string
	base := func(_ context.Context, _ string, _ []string, name string, args []string) ([]byte, []byte, int, error) {
		gotName, gotArgs = name, args
		return nil, nil, 0, nil
	}
	_, _, _, _ = wrapIOPolicy(borg.Runner(base), IOPolicy{})(context.Background(), "", nil, "borg", []string{"check", "/r"})
	if gotName != "borg" || !reflect.DeepEqual(gotArgs, []string{"check", "/r"}) {
		t.Errorf("inert policy altered the command: %q %v", gotName, gotArgs)
	}
}

func TestWrapIOPolicyDecorates(t *testing.T) {
	t.Parallel()
	var gotName string
	var gotArgs []string
	base := func(_ context.Context, _ string, _ []string, name string, args []string) ([]byte, []byte, int, error) {
		gotName, gotArgs = name, args
		return nil, nil, 0, nil
	}
	p := IOPolicy{NiceCPU: 12, IOClass: "idle"}
	_, _, _, _ = wrapIOPolicy(borg.Runner(base), p)(context.Background(), "", nil, "borg", []string{"check", "/r"})
	want := []string{"-n", "12", "ionice", "-c", "3", "borg", "check", "/r"}
	if gotName != "nice" || !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("wrapped = %q %v, want nice %v", gotName, gotArgs, want)
	}
}

// nice/ionice must reach every borg invocation a real run spawns.
func TestRunBorgL1AppliesIOPolicy(t *testing.T) {
	t.Parallel()
	var calls [][]string
	capture := func(_ context.Context, _ string, _ []string, name string, args []string) ([]byte, []byte, int, error) {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		if borgSub(call) == "list" && containsArg(call, "--json") {
			return []byte(borgListJSON(base.Add(-1 * time.Hour))), nil, 0, nil
		}
		return nil, nil, 0, nil
	}
	e := NewLocal("h")
	e.borgRunner = capture
	e.WithIOPolicy(IOPolicy{NiceCPU: 10, IOClass: "idle"})

	if _, err := e.RunStep(context.Background(), borgStep(borgL1(), base)); err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("no borg invocations recorded")
	}
	for _, call := range calls {
		if call[0] != "nice" || !containsArg(call, "ionice") || !containsArg(call, "borg") {
			t.Errorf("call not IO-wrapped: %v", call)
		}
	}
}

func borgSub(call []string) string {
	known := map[string]bool{"list": true, "info": true, "check": true, "extract": true}
	for _, a := range call {
		if known[a] {
			return a
		}
	}
	return ""
}

func containsArg(call []string, want string) bool {
	for _, a := range call {
		if a == want {
			return true
		}
	}
	return false
}
