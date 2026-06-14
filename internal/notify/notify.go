package notify

import (
	"context"
	"io"
	"log/slog"
	"time"
)

type Event string

const (
	EventFail    Event = "fail"
	EventError   Event = "error"
	EventRecover Event = "recover"
	EventStale   Event = "stale"
)

type Notification struct {
	Event       Event
	Drill       string
	Level       string        // level reached / where it failed
	Detail      string        // the diagnosis specifics
	LastProven  time.Time     // zero = never proven
	MaxProofAge time.Duration // for stale messages
	Now         time.Time
}

type Sender interface {
	Send(ctx context.Context, title, body string) error
}

type Notifier struct {
	sender  Sender
	enabled map[Event]bool
	log     *slog.Logger
}

// New builds a Notifier; with no URLs it returns (nil, nil), and a nil *Notifier
// dispatches nothing, so callers need no special-casing.
func New(urls, events []string, log *slog.Logger) (*Notifier, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	s, err := newShoutrrrSender(urls)
	if err != nil {
		return nil, err
	}
	return NewWithSender(s, events, log), nil
}

// NewWithSender builds a Notifier over a caller-supplied Sender (the seam for a
// custom transport or a test fake).
func NewWithSender(s Sender, events []string, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	en := make(map[Event]bool, len(events))
	for _, e := range events {
		en[Event(e)] = true
	}
	return &Notifier{sender: s, enabled: en, log: log}
}

// Dispatch renders and sends note when its event is enabled. A failed send is
// logged, never returned: a broken notifier must not break a run.
func (n *Notifier) Dispatch(ctx context.Context, note Notification) {
	if n == nil || !n.enabled[note.Event] {
		return
	}
	title, body := Render(note)
	if err := n.sender.Send(ctx, title, body); err != nil {
		n.log.Warn("notification send failed", "event", string(note.Event), "drill", note.Drill, "error", err.Error())
	}
}

// ClassifyRun maps a finished run to its event from the previous result and the
// drill's stale flag; ok=false means stay silent (e.g. pass after pass). Results
// are the store's strings, keeping notify decoupled from the store.
func ClassifyRun(prev, current string, wasStale bool) (Event, bool) {
	switch current {
	case "fail":
		return EventFail, true
	case "error":
		return EventError, true
	case "pass":
		if wasStale || prev == "fail" || prev == "error" {
			return EventRecover, true
		}
	}
	return "", false
}

// Validate reports whether the shoutrrr URLs parse, without sending.
func Validate(urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	_, err := newShoutrrrSender(urls)
	return err
}
