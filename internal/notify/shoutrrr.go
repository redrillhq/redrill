package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

const sendTimeout = 15 * time.Second

type shoutrrrSender struct {
	router *router.ServiceRouter
}

func newShoutrrrSender(urls []string) (*shoutrrrSender, error) {
	sr, err := shoutrrr.CreateSender(urls...)
	if err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	sr.Timeout = sendTimeout
	return &shoutrrrSender{router: sr}, nil
}

// Send delivers body (titled via the standard shoutrrr "title" param) to every
// configured service. shoutrrr bounds the call by its own Timeout rather than
// ctx, so ctx is accepted for the interface but not forwarded.
func (s *shoutrrrSender) Send(_ context.Context, title, body string) error {
	params := types.Params{"title": title}
	return errors.Join(s.router.Send(body, &params)...)
}
