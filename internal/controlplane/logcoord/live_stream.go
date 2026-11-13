package logcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	LiveSessionDuration        = 45 * time.Second
	LiveQueueSize              = 64
	LiveEventBytes             = 64 << 10
	LiveSessionBytes    uint64 = 8 << 20
	LiveSessionRows            = 10000
	LiveWorkerLimit            = 8
)

var ErrLiveUnsupported = errors.New("native live stream unsupported")
var ErrLiveSlowConsumer = errors.New("live stream consumer too slow")
var ErrLiveLimit = errors.New("live stream budget exhausted")

// LiveWorkerRequest deliberately carries no historical Worker view/cursor:
// source membership is frozen here, while future days and streams may appear.
type LiveWorkerRequest struct {
	WorkerSearchRequest
	PermissionScope string
}
type NativeLiveWorker interface {
	LiveTail(context.Context, LiveWorkerRequest, func(Event) error) error
}

type LiveFrame struct {
	Type                string `json:"type"`
	Event               *Event `json:"event,omitempty"`
	Code                string `json:"code,omitempty"`
	Message             string `json:"message,omitempty"`
	WorkerID            string `json:"workerId,omitempty"`
	ViewID              string `json:"viewId,omitempty"`
	Partial             bool   `json:"partial,omitempty"`
	ReplayWindowSeconds int    `json:"replayWindowSeconds,omitempty"`
}

// LiveTail forwards decoded native RPC messages immediately, without buffering
// the entire stream into SearchResponse and without polling Coordinator.Tail.
func (a *WorkerClientAdapter) LiveTail(ctx context.Context, req LiveWorkerRequest, consume func(Event) error) error {
	if a == nil || a.client == nil {
		return ErrLiveUnsupported
	}
	base := toProtoQueryBase(req.WorkerSearchRequest)
	base.PermissionScope = req.PermissionScope
	stream, err := a.client.LogTailLive(ctx, &workerpb.LogTailRequest{Query: base, Mode: workerpb.LogTailMode_LOG_TAIL_FOLLOW_LIVE})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if status.Code(err) == codes.Unimplemented {
			return fmt.Errorf("%w: %v", ErrLiveUnsupported, err)
		}
		if err != nil {
			return err
		}
		if err = consume(eventFromProto(ev)); err != nil {
			return err
		}
	}
}

// Live opens at most eight native Worker subscriptions with one finite shared
// queue. A blocked HTTP writer cannot turn slow consumers into unbounded heaps.
// send is only called by this goroutine; producers use a non-blocking enqueue.
func (c *Coordinator) Live(ctx context.Context, q Query, send func(LiveFrame) error) error {
	if q.ToUTC != "" || q.Cursor != "" {
		return fmt.Errorf("live stream requires an open end and no historical cursor")
	}
	// New subscriptions need a frozen authorised member set, not historical
	// Worker views/read pins. Explicit view reuse still goes through full checks.
	view, resolved, err := c.resolveForQueryFast(ctx, q)
	if err != nil {
		return err
	}
	order, by := resolved.ByWorker()
	type source struct {
		worker  string
		targets []string
	}
	var sources []source
	partial := false
	for _, worker := range order {
		seen := map[string]bool{}
		var ids []string
		for _, target := range by[worker] {
			id := workerTargetID(target)
			if !strings.HasPrefix(id, "inst:") {
				continue
			}
			if target.Readiness != ReadyOnline {
				partial = true
				continue
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			sources = append(sources, source{worker, ids})
		}
	}
	if len(sources) == 0 {
		return fmt.Errorf("%w: no online instance log targets", ErrLiveUnsupported)
	}
	maxWorkers := LiveWorkerLimit
	if q.Budget.MaxFanout > 0 && int(q.Budget.MaxFanout) < maxWorkers {
		maxWorkers = int(q.Budget.MaxFanout)
	}
	if len(sources) > maxWorkers {
		return ErrLiveLimit
	}
	duration := LiveSessionDuration
	if q.Budget.TimeoutMS > 0 && time.Duration(q.Budget.TimeoutMS)*time.Millisecond < duration {
		duration = time.Duration(q.Budget.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	limit := LiveSessionRows
	if q.Budget.Limit > 0 && q.Budget.Limit < limit {
		limit = q.Budget.Limit
	}
	byteLimit := LiveSessionBytes
	if q.Budget.MaxBytes > 0 && q.Budget.MaxBytes < byteLimit {
		byteLimit = q.Budget.MaxBytes
	}
	queue := make(chan LiveFrame, LiveQueueSize)
	fail := make(chan error, 1)
	pushFailure := func(err error) {
		select {
		case fail <- err:
		default:
		}
		cancel()
	}
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(src source) {
			defer wg.Done()
			client, err := c.dialer.Dial(ctx, src.worker)
			if err != nil {
				pushFailure(err)
				return
			}
			live, ok := client.(NativeLiveWorker)
			if !ok {
				pushFailure(ErrLiveUnsupported)
				return
			}
			perBytes := byteLimit / uint64(len(sources))
			if perBytes == 0 {
				pushFailure(ErrLiveLimit)
				return
			}
			req := LiveWorkerRequest{WorkerSearchRequest: WorkerSearchRequest{TargetIDs: src.targets, FromUTC: q.FromUTC, Filter: q.Filter, Budget: QueryBudget{Limit: limit, MaxBytes: perBytes, TimeoutMS: uint32(duration / time.Millisecond)}}, PermissionScope: q.PermissionScope}
			err = live.LiveTail(ctx, req, func(ev Event) error {
				allowed := false
				for _, id := range src.targets {
					if ev.LogSourceID == id || strings.HasPrefix(ev.LogSourceID, id+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					return ErrViewAuthz
				}
				ev.WorkerID = src.worker
				raw, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				if len(raw) > LiveEventBytes {
					return ErrLiveLimit
				}
				frame := LiveFrame{Type: "event", Event: &ev}
				select {
				case queue <- frame:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				default:
					return ErrLiveSlowConsumer
				}
			})
			if err != nil && ctx.Err() == nil {
				pushFailure(err)
			}
		}(src)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	if err := send(LiveFrame{Type: "ready", ViewID: view.ViewID, Partial: partial, ReplayWindowSeconds: 10, Message: "native live tail; reconnect replay is best effort, not a durable cursor"}); err != nil {
		return err
	}
	var rows int
	var used uint64
	for {
		select {
		case err := <-fail:
			return err
		case <-ctx.Done():
			select {
			case err := <-fail:
				return err
			default:
				return ctx.Err()
			}
		case <-done:
			select {
			case err := <-fail:
				return err
			default:
				return io.ErrUnexpectedEOF
			}
		case frame := <-queue:
			raw, _ := json.Marshal(frame)
			used += uint64(len(raw))
			rows++
			if rows > limit || used > byteLimit {
				return ErrLiveLimit
			}
			if err := send(frame); err != nil {
				return err
			}
		}
	}
}

func LiveErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrLiveUnsupported), status.Code(err) == codes.Unimplemented:
		return "UNSUPPORTED"
	case errors.Is(err, ErrViewAuthz), status.Code(err) == codes.PermissionDenied:
		return "FORBIDDEN"
	case status.Code(err) == codes.InvalidArgument:
		return "INVALID_REQUEST"
	case errors.Is(err, ErrLiveSlowConsumer):
		return "SLOW_CONSUMER"
	case errors.Is(err, ErrLiveLimit), status.Code(err) == codes.ResourceExhausted:
		return "BUDGET_EXCEEDED"
	case errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return "SESSION_EXPIRED"
	case errors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return "CANCELLED"
	default:
		return "STREAM_UNAVAILABLE"
	}
}
