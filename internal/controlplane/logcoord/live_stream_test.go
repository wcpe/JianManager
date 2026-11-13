package logcoord

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

type liveFixtureWorker struct {
	WorkerClient
	stream func(context.Context, LiveWorkerRequest, func(Event) error) error
}

func (f *liveFixtureWorker) LiveTail(ctx context.Context, req LiveWorkerRequest, send func(Event) error) error {
	return f.stream(ctx, req, send)
}

func liveFixture(t *testing.T, stream func(context.Context, LiveWorkerRequest, func(Event) error) error) (*Coordinator, Query) {
	t.Helper()
	base := &fakeWorker{workerID: "w1", searchTarget: []WorkerTargetResult{{TargetID: "inst:1", State: CoverageSuccess}}}
	worker := &liveFixtureWorker{WorkerClient: base, stream: stream}
	dialer := &liveFixtureDialer{client: worker}
	c := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	return c, Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "u1", PermissionScope: "scope", Budget: QueryBudget{Limit: 10, TimeoutMS: 1000}}
}

type liveFixtureDialer struct{ client WorkerClient }

func (d *liveFixtureDialer) Dial(context.Context, string) (WorkerClient, error) { return d.client, nil }

func TestLiveForwardsBeforeEOFAndReleasesProducerOnCancel(t *testing.T) {
	stopped := make(chan struct{})
	c, q := liveFixture(t, func(ctx context.Context, req LiveWorkerRequest, send func(Event) error) error {
		defer close(stopped)
		require.Equal(t, []string{"inst:1"}, req.TargetIDs)
		require.Equal(t, "scope", req.PermissionScope)
		require.Empty(t, req.ViewID)
		if err := send(Event{EventID: "e1", LogSourceID: "inst:1/file", Message: "hello"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var frames []LiveFrame
	err := c.Live(ctx, q, func(f LiveFrame) error {
		frames = append(frames, f)
		if f.Type == "event" {
			cancel()
		}
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, frames, 2)
	require.Equal(t, "ready", frames[0].Type)
	require.Equal(t, "e1", frames[1].Event.EventID)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("producer was not cancelled")
	}
}

func TestLiveRejectsOutOfScopeRows(t *testing.T) {
	c, q := liveFixture(t, func(ctx context.Context, req LiveWorkerRequest, send func(Event) error) error {
		return send(Event{LogSourceID: "inst:2/file"})
	})
	err := c.Live(context.Background(), q, func(f LiveFrame) error { require.NotEqual(t, "event", f.Type); return nil })
	require.ErrorIs(t, err, ErrViewAuthz)
}

func TestLiveQueueHasHardLimitForSlowConsumer(t *testing.T) {
	var pushed atomic.Int32
	c, q := liveFixture(t, func(ctx context.Context, req LiveWorkerRequest, send func(Event) error) error {
		for i := 0; i < LiveQueueSize+2; i++ {
			if err := send(Event{LogSourceID: "inst:1/file"}); err != nil {
				return err
			}
			pushed.Add(1)
		}
		return errors.New("unexpected success")
	})
	err := c.Live(context.Background(), q, func(f LiveFrame) error {
		if f.Type == "ready" {
			deadline := time.After(time.Second)
			for pushed.Load() < LiveQueueSize {
				select {
				case <-deadline:
					t.Fatal("producer did not fill queue")
				default:
					time.Sleep(time.Millisecond)
				}
			}
		}
		return nil
	})
	require.ErrorIs(t, err, ErrLiveSlowConsumer)
	require.Equal(t, int32(LiveQueueSize), pushed.Load())
}

func TestLiveRejectsHistoricalCursorAndWrongView(t *testing.T) {
	c, q := liveFixture(t, func(context.Context, LiveWorkerRequest, func(Event) error) error { return nil })
	q.Cursor = "historical"
	require.Error(t, c.Live(context.Background(), q, func(LiveFrame) error { return nil }))
	q.Cursor = ""
	q.ViewID = "foreign"
	require.Error(t, c.Live(context.Background(), q, func(LiveFrame) error { return nil }))
}

type metricPBFixture struct {
	workerpb.WorkerServiceClient
	request *workerpb.LogStatsRequest
}

func (f *metricPBFixture) LogStats(_ context.Context, r *workerpb.LogStatsRequest, _ ...grpc.CallOption) (*workerpb.LogStatsResponse, error) {
	f.request = r
	return &workerpb.LogStatsResponse{}, nil
}
func TestStatsMetricFieldReachesProtoAdapter(t *testing.T) {
	pb := &metricPBFixture{}
	_, err := NewWorkerClientAdapter(pb).Stats(context.Background(), WorkerSearchRequest{TargetIDs: []string{"inst:1"}, MetricField: "duration_ms"}, []string{"level"}, "1m")
	require.NoError(t, err)
	require.Equal(t, "duration_ms", pb.request.MetricField)
	require.Equal(t, "1m", pb.request.TimeBucket)
}

func TestStructuredFieldsSurviveAdapterAndConsumeByteBudget(t *testing.T) {
	original := &workerpb.LogEvent{EventId: "fields", LogSourceId: "inst:1/file", Fields: map[string]string{"trace_id": "abc", "duration_ms": "12"}}
	row := eventFromProto(original)
	require.Equal(t, "abc", row.Fields["trace_id"])
	original.Fields["trace_id"] = "changed"
	require.Equal(t, "abc", row.Fields["trace_id"], "protobuf map must not alias returned event fields")
	base := Event{EventID: "fields", LogSourceID: "inst:1/file"}
	row.Fields["large"] = strings.Repeat("x", 1024)
	require.Greater(t, row.ApproxBytes(), base.ApproxBytes()+uint64(1024))
	c, q := liveFixture(t, func(ctx context.Context, req LiveWorkerRequest, send func(Event) error) error {
		return send(Event{LogSourceID: "inst:1/file", Fields: map[string]string{"large": strings.Repeat("x", LiveEventBytes)}})
	})
	require.ErrorIs(t, c.Live(context.Background(), q, func(f LiveFrame) error { require.NotEqual(t, "event", f.Type); return nil }), ErrLiveLimit)
}
