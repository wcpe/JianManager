package grpcsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcmap"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LogTailLive is a distinct RPC so an older Worker returns Unimplemented instead
// of silently serving one bounded snapshot under an unknown mode.
func (s *Service) LogTailLive(req *workerpb.LogTailRequest, stream workerpb.WorkerService_LogTailLiveServer) error {
	if !s.plannerReady() {
		return status.Error(codes.Unimplemented, "native live tail unavailable")
	}
	subscriber, ok := s.query.(query.NativeLiveClient)
	if !ok {
		return status.Error(codes.Unimplemented, "native live tail unavailable")
	}
	in := grpcmap.QueryRequestFromBase(req.GetQuery())
	if in.ProtocolVersion != "" && in.ProtocolVersion != query.DefaultProtocolVersion {
		return status.Error(codes.Unimplemented, "unsupported log protocol")
	}
	if err := query.ValidateLiveRequest(in); err != nil {
		code := codes.InvalidArgument
		if errors.Is(err, query.ErrLiveBudget) {
			code = codes.ResourceExhausted
		}
		if errors.Is(err, query.ErrLiveUnauthorized) {
			code = codes.PermissionDenied
		}
		return status.Error(code, err.Error())
	}
	err := subscriber.LiveTail(stream.Context(), in, func(ev logtypes.Event) error {
		return stream.Send(grpcmap.TailResponseItems(query.TailResponse{Items: []logtypes.Event{ev}})[0])
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, query.ErrRangeClientUnimplemented):
		return status.Error(codes.Unimplemented, "native live tail unavailable")
	case errors.Is(err, query.ErrLiveBudget):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, query.ErrLiveUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "live tail cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "live tail session expired; reconnect may have gaps")
	default:
		return status.Error(codes.Unavailable, fmt.Sprintf("native live tail: %v", err))
	}
}
