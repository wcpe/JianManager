package vlrange

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// LiveTail opens one real VictoriaLogs native tail HTTP response. Authorisation
// is in extra_filters (independent from user OR/filter syntax), and every decoded
// row is checked again before it can leave the Worker.
func (c *Client) LiveTail(ctx context.Context, req query.QueryRequest, consume func(logtypes.Event) error) error {
	if err := query.ValidateLiveRequest(req); err != nil {
		return err
	}
	if c == nil || c.vl == nil || consume == nil {
		return query.ErrRangeClientUnimplemented
	}
	budget := query.LiveBudget(req.Budget)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(budget.TimeoutMS)*time.Millisecond)
	defer cancel()
	selectors := make([]string, 0, len(req.AuthorizedTargets))
	for _, target := range req.AuthorizedTargets {
		selectors = append(selectors, "log_source_id:~"+strconv.Quote("^"+regexp.QuoteMeta(target)+"(/.*)?$"))
	}
	scope := "(" + strings.Join(selectors, " OR ") + ")"
	if req.TimeRange.FromUTC != "" {
		scope += " AND _time:>=" + strconv.Quote(req.TimeRange.FromUTC)
	}
	if req.TimeRange.ToUTC != "" {
		scope += " AND _time:<" + strconv.Quote(req.TimeRange.ToUTC)
	}
	expr := strings.TrimSpace(req.Filter)
	if expr == "" {
		expr = "*"
	}
	params := url.Values{"query": {expr}, "extra_filters": {scope}, "start_offset": {"10s"}, "offset": {"1s"}, "refresh_interval": {"1s"}}
	var used uint64
	var rows uint32
	read, err := c.vl.StreamBudget(ctx, "/select/logsql/tail", params, budget.MaxBytes, func(reader io.Reader) error {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), query.LiveMaxLineBytes)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			line := scanner.Bytes()
			used += uint64(len(line) + 1)
			if used >= budget.MaxBytes || rows >= budget.Limit {
				return query.ErrLiveBudget
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			ev, err := eventFromJSON(line)
			if err != nil {
				return err
			}
			if !query.LiveSourceAllowed(req.AuthorizedTargets, ev.Source.LogSourceID) {
				return query.ErrLiveUnauthorized
			}
			if ev.EventTimeUTC != "" {
				ts, parseErr := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
				if parseErr != nil {
					return fmt.Errorf("vlrange: invalid native event time")
				}
				if req.TimeRange.FromUTC != "" {
					from, _ := time.Parse(time.RFC3339Nano, req.TimeRange.FromUTC)
					if ts.Before(from) {
						continue
					}
				}
				if req.TimeRange.ToUTC != "" {
					to, _ := time.Parse(time.RFC3339Nano, req.TimeRange.ToUTC)
					if !ts.Before(to) {
						continue
					}
				}
			}
			if err := consume(ev); err != nil {
				return err
			}
			rows++
		}
		if errors.Is(scanner.Err(), bufio.ErrTooLong) {
			return query.ErrLiveBudget
		}
		return scanner.Err()
	})
	if err != nil {
		return err
	}
	if read >= budget.MaxBytes {
		return query.ErrLiveBudget
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return io.ErrUnexpectedEOF
}

func (t *Tiered) LiveTail(ctx context.Context, req query.QueryRequest, consume func(logtypes.Event) error) error {
	c, err := t.forRange(ctx, query.AuthoritativeRange{Owner: catalog.OwnerHot})
	if err != nil {
		return err
	}
	return c.LiveTail(ctx, req, consume)
}

var _ query.NativeLiveClient = (*Client)(nil)
var _ query.NativeLiveClient = (*Tiered)(nil)
