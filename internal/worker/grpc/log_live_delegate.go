package grpc

import "github.com/wcpe/JianManager/proto/workerpb"

func (s *Server) LogTailLive(req *workerpb.LogTailRequest, stream workerpb.WorkerService_LogTailLiveServer) error {
	if q := s.logRPC(); q != nil {
		return q.LogTailLive(req, stream)
	}
	return s.UnimplementedWorkerServiceServer.LogTailLive(req, stream)
}
