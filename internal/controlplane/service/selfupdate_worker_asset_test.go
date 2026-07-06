package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func sha256String(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSelfUpdateWorkerAsset_EnsureCachesAndHits(t *testing.T) {
	body := "worker-binary"
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x", AllowInsecure: true}, newTestRoot(t))
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: version.Version, Artifacts: []FeedArtifact{{
			Component: ComponentWorker,
			OS:        "linux",
			Arch:      "amd64",
			URL:       ts.URL,
			SHA256:    sha256String(body),
		}}}, nil
	}

	first, err := svc.EnsureWorkerAsset(context.Background(), "linux", "amd64")
	require.NoError(t, err)
	second, err := svc.EnsureWorkerAsset(context.Background(), "linux", "amd64")
	require.NoError(t, err)

	require.Equal(t, int64(len(body)), first.Size)
	require.Equal(t, first.Path, second.Path)
	require.Equal(t, 1, hits, "缓存命中时不应重复下载")
	require.FileExists(t, filepath.Join(svc.workerAssetDir(version.Version, "linux", "amd64"), "metadata.json"))
}

func TestSelfUpdateWorkerAsset_EnsureRedownloadsCorruptCache(t *testing.T) {
	body := "worker-binary"
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x", AllowInsecure: true}, newTestRoot(t))
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: version.Version, Artifacts: []FeedArtifact{{
			Component: ComponentWorker,
			OS:        "linux",
			Arch:      "amd64",
			URL:       ts.URL,
			SHA256:    sha256String(body),
		}}}, nil
	}

	entry, err := svc.EnsureWorkerAsset(context.Background(), "linux", "amd64")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(entry.Path, []byte("broken"), 0o755))

	entry, err = svc.EnsureWorkerAsset(context.Background(), "linux", "amd64")
	require.NoError(t, err)
	got, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
	require.Equal(t, 2, hits, "缓存损坏时应重新下载并校验")
}

func TestSelfUpdateWorkerAsset_TokenScope(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, newTestRoot(t))
	token, err := svc.IssueWorkerAssetToken(WorkerAssetTokenScope{
		Version:  version.Version,
		OS:       "linux",
		Arch:     "amd64",
		Purpose:  WorkerAssetPurposeUpgrade,
		NodeUUID: "node-1",
	})
	require.NoError(t, err)

	scope, err := svc.ValidateWorkerAssetToken(token, WorkerAssetTokenScope{
		Version:  version.Version,
		OS:       "linux",
		Arch:     "amd64",
		Purpose:  WorkerAssetPurposeUpgrade,
		NodeUUID: "node-1",
	})
	require.NoError(t, err)
	require.Equal(t, "node-1", scope.NodeUUID)

	_, err = svc.ValidateWorkerAssetToken(token, WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "windows",
		Arch:    "amd64",
		Purpose: WorkerAssetPurposeUpgrade,
	})
	require.ErrorIs(t, err, ErrWorkerAssetTokenInvalid)
}

func TestSelfUpdateWorkerAsset_UpgradeTokenRequiresNodeUUID(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, newTestRoot(t))

	_, err := svc.IssueWorkerAssetToken(WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "linux",
		Arch:    "amd64",
		Purpose: WorkerAssetPurposeUpgrade,
	})
	require.Error(t, err)
}

type fakeUpgradeWorker struct {
	workerpb.WorkerServiceClient
	got *workerpb.UpgradeWorkerRequest
}

func (f *fakeUpgradeWorker) UpgradeWorker(_ context.Context, in *workerpb.UpgradeWorkerRequest, _ ...grpc.CallOption) (*workerpb.UpgradeWorkerResponse, error) {
	f.got = in
	return &workerpb.UpgradeWorkerResponse{Success: true, FromVersion: "0.12.0"}, nil
}

func TestSelfUpdateUpgradeNode_UsesCachedWorkerAssetURL(t *testing.T) {
	body := "worker-binary"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	db := newSelfUpdateTestDB(t)
	node := &model.Node{
		UUID:     "node-upgrade",
		Name:     "n1",
		Host:     "127.0.0.1",
		GRPCPort: 1,
		WSPort:   2,
		Secret:   "s",
		OS:       "linux",
		Arch:     "amd64",
		Status:   model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(node).Error)
	pool := cpgrpc.NewClientPool()
	fake := &fakeUpgradeWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x", AllowInsecure: true}, newTestRoot(t))
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: version.Version, Artifacts: []FeedArtifact{{
			Component: ComponentWorker,
			OS:        "linux",
			Arch:      "amd64",
			URL:       ts.URL,
			SHA256:    sha256String(body),
		}}}, nil
	}

	from, to, err := svc.UpgradeNodeWithBaseURL(context.Background(), node.ID, "", "http://cp.local")
	require.NoError(t, err)
	require.Equal(t, "0.12.0", from)
	require.Equal(t, version.Version, to)
	require.NotNil(t, fake.got)
	require.Equal(t, sha256String(body), fake.got.Sha256)
	require.True(t, strings.HasPrefix(fake.got.DownloadUrl, "http://cp.local/worker-assets/"+version.Version+"/linux/amd64/worker?token="), fake.got.DownloadUrl)
	require.True(t, fake.got.AllowInsecure, "CP-local http 下载应沿用 allow_insecure")
}
