package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// NativeLiveClient is deliberately separate from the older bounded Tail call.
// Its callback is invoked while the remote stream is still open, not after a
// repeated Search has accumulated a batch.
type NativeLiveClient interface {
	LiveTail(context.Context, QueryRequest, func(logtypes.Event) error) error
}

var (
	ErrLiveBudget       = errors.New("live tail budget exhausted")
	ErrLiveUnauthorized = errors.New("live tail unauthorized source")
	liveInstanceTarget  = regexp.MustCompile(`^inst:[1-9][0-9]*$`)
)

const (
	LiveMaxDuration         = 45 * time.Second
	LiveMaxRows      uint32 = 10000
	LiveMaxBytes     uint64 = 8 << 20
	LiveMaxLineBytes        = 64 << 10
	LiveMaxTargets          = 256
)

// ValidateLiveRequest checks the finite source set before any network I/O.
// Native tail operates on changing data, so a fixed historical Worker view is
// not accepted here. The CP validates its own view before opening subscriptions.
func ValidateLiveRequest(req QueryRequest) error {
	if len(req.AuthorizedTargets) == 0 || len(req.AuthorizedTargets) > LiveMaxTargets {
		return ErrLiveUnauthorized
	}
	for _, target := range req.AuthorizedTargets {
		if !liveInstanceTarget.MatchString(target) {
			return ErrLiveUnauthorized
		}
	}
	if req.View != nil && req.View.ViewID != "" {
		return fmt.Errorf("%w: native live tail cannot reuse a historical worker view", ErrLiveUnauthorized)
	}
	if len(req.Filter) > 8<<10 {
		return fmt.Errorf("%w: expression too long", ErrLiveBudget)
	}
	var quoted byte
	escaped := false
	for i := 0; i < len(req.Filter); i++ {
		c := req.Filter[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted != 0 {
			if c == '\\' && quoted != '`' {
				escaped = true
				continue
			}
			if c == quoted {
				quoted = 0
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			quoted = c
			continue
		}
		if c == '|' {
			return fmt.Errorf("unsupported live tail expression pipe")
		}
	}
	if quoted != 0 {
		return fmt.Errorf("unsupported live tail expression: unclosed quoted value")
	}
	for _, raw := range []string{req.TimeRange.FromUTC, req.TimeRange.ToUTC} {
		if raw != "" {
			if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
				return fmt.Errorf("invalid live tail time range: %w", err)
			}
		}
	}
	return nil
}

func LiveBudget(input Budget) Budget {
	if input.Limit == 0 || input.Limit > LiveMaxRows {
		input.Limit = LiveMaxRows
	}
	if input.MaxBytes == 0 || input.MaxBytes > LiveMaxBytes {
		input.MaxBytes = LiveMaxBytes
	}
	maxMS := uint32(LiveMaxDuration / time.Millisecond)
	if input.TimeoutMS == 0 || input.TimeoutMS > maxMS {
		input.TimeoutMS = maxMS
	}
	return input
}

func LiveSourceAllowed(targets []string, source string) bool {
	for _, target := range targets {
		if source == target || strings.HasPrefix(source, target+"/") {
			return true
		}
	}
	return false
}

// LiveTail uses the managed native subscriber without pinning a day partition.
// The authorised instance set stays fixed while new UTC days/streams may appear.
func (s *Service) LiveTail(ctx context.Context, req QueryRequest, consume func(logtypes.Event) error) error {
	if err := ValidateLiveRequest(req); err != nil {
		return err
	}
	if err := s.foregroundAdmission(); err != nil {
		return err
	}
	client, ok := s.client.(NativeLiveClient)
	if !ok {
		return ErrRangeClientUnimplemented
	}
	req.Budget = LiveBudget(req.Budget)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Budget.TimeoutMS)*time.Millisecond)
	defer cancel()
	return client.LiveTail(ctx, req, consume)
}
