package grpcsvc

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type nativeFixtureQuery struct {
	fakeQuery
	called bool
}

func (f *nativeFixtureQuery) LiveTail(ctx context.Context, in query.QueryRequest, send func(logtypes.Event) error) error {
	f.called = true
	return send(sampleEV("live"))
}
func TestLogTailLiveValidatesWorkerScopeAndDoesNotCallSnapshotTail(t *testing.T) {
	f := &nativeFixtureQuery{}
	s := New(f, nil)
	for _, targets := range [][]string{nil, {"*"}, {"node:1"}} {
		err := s.LogTailLive(&workerpb.LogTailRequest{Query: &workerpb.LogQueryRequestBase{AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: targets}}}, &fakeTailStream{ctx: context.Background()})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.False(t, f.called)
	}
	stream := &fakeTailStream{ctx: context.Background()}
	err := s.LogTailLive(&workerpb.LogTailRequest{Query: &workerpb.LogQueryRequestBase{AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"inst:1"}}}}, stream)
	require.NoError(t, err)
	require.True(t, f.called)
	require.Zero(t, f.tailCalls)
	require.Len(t, stream.items, 1)
}
func TestLogTailLiveOlderQueryProviderReturnsUnimplemented(t *testing.T) {
	err := New(&fakeQuery{}, nil).LogTailLive(&workerpb.LogTailRequest{}, &fakeTailStream{ctx: context.Background()})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
