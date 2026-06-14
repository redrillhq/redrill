package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSender struct {
	calls []sent
	err   error
}

type sent struct{ title, body string }

func (f *fakeSender) Send(_ context.Context, title, body string) error {
	f.calls = append(f.calls, sent{title, body})
	return f.err
}

func TestClassifyRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prev     string
		cur      string
		wasStale bool
		want     Event
		ok       bool
	}{
		{"fail fires", "pass", "fail", false, EventFail, true},
		{"error fires", "pass", "error", false, EventError, true},
		{"first pass is silent", "", "pass", false, "", false},
		{"pass after pass is silent", "pass", "pass", false, "", false},
		{"recover from fail", "fail", "pass", false, EventRecover, true},
		{"recover from error", "error", "pass", false, EventRecover, true},
		{"recover from stale", "pass", "pass", true, EventRecover, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ClassifyRun(tt.prev, tt.cur, tt.wasStale)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ClassifyRun(%q,%q,%v) = %q,%v; want %q,%v", tt.prev, tt.cur, tt.wasStale, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDispatchFiltersEvents(t *testing.T) {
	t.Parallel()
	f := &fakeSender{}
	n := NewWithSender(f, []string{"fail", "error"}, nil)
	n.Dispatch(context.Background(), Notification{Event: EventFail, Drill: "d", Now: time.Now()})
	n.Dispatch(context.Background(), Notification{Event: EventRecover, Drill: "d", Now: time.Now()}) // not enabled
	if len(f.calls) != 1 {
		t.Fatalf("sends = %d, want 1 (recover not enabled)", len(f.calls))
	}
	if f.calls[0].title == "" {
		t.Error("empty title")
	}
}

func TestDispatchNilNotifierNoop(t *testing.T) {
	t.Parallel()
	var n *Notifier
	n.Dispatch(context.Background(), Notification{Event: EventFail}) // must not panic
}

// A failed send is swallowed: a broken notifier must not break a run.
func TestDispatchSendErrorSwallowed(t *testing.T) {
	t.Parallel()
	f := &fakeSender{err: errors.New("network down")}
	n := NewWithSender(f, []string{"fail"}, nil)
	n.Dispatch(context.Background(), Notification{Event: EventFail, Drill: "d", Now: time.Now()})
	if len(f.calls) != 1 {
		t.Fatalf("want the send attempted once, got %d", len(f.calls))
	}
}
