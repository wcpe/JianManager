package process

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeImageClient 实现 dockerImageClient，记录调用并返回预置数据。
type fakeImageClient struct {
	images      []imagetypes.Summary
	pulled      []string
	removed     []string
	pullErr     error
	removeErr   error
	listErr     error
	closeCalled bool
}

func (f *fakeImageClient) ImageList(_ context.Context, _ imagetypes.ListOptions) ([]imagetypes.Summary, error) {
	return f.images, f.listErr
}

func (f *fakeImageClient) ImagePull(_ context.Context, ref string, _ imagetypes.PullOptions) (io.ReadCloser, error) {
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	f.pulled = append(f.pulled, ref)
	return io.NopCloser(strings.NewReader("progress")), nil
}

func (f *fakeImageClient) ImageRemove(_ context.Context, ref string, _ imagetypes.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	f.removed = append(f.removed, ref)
	return []imagetypes.DeleteResponse{{Deleted: ref}}, nil
}

func (f *fakeImageClient) Close() error {
	f.closeCalled = true
	return nil
}

// withFakeImageClient 临时把 newImageClient 替换为返回指定 fake，并在测试结束后还原。
func withFakeImageClient(t *testing.T, fake *fakeImageClient) {
	t.Helper()
	orig := newImageClient
	newImageClient = func() (dockerImageClient, error) { return fake, nil }
	t.Cleanup(func() { newImageClient = orig })
}

func TestListDockerImages(t *testing.T) {
	fake := &fakeImageClient{images: []imagetypes.Summary{
		{ID: "sha256:abc", RepoTags: []string{"itzg/minecraft-server:latest"}, Size: 1234, Created: 100},
		{ID: "sha256:def", RepoTags: []string{"alpine:3.20"}, Size: 5, Created: 200},
	}}
	withFakeImageClient(t, fake)

	out, err := ListDockerImages(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "sha256:abc", out[0].ID)
	assert.Equal(t, []string{"itzg/minecraft-server:latest"}, out[0].Tags)
	assert.Equal(t, int64(1234), out[0].SizeBytes)
	assert.True(t, fake.closeCalled, "客户端应被关闭")
}

func TestListDockerImages_DockerUnavailable(t *testing.T) {
	orig := newImageClient
	newImageClient = func() (dockerImageClient, error) { return nil, errors.New("docker 不可达") }
	t.Cleanup(func() { newImageClient = orig })

	_, err := ListDockerImages(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker 不可达")
}

func TestPullDockerImage(t *testing.T) {
	fake := &fakeImageClient{}
	withFakeImageClient(t, fake)

	require.NoError(t, PullDockerImage(context.Background(), "itzg/minecraft-server:latest"))
	assert.Equal(t, []string{"itzg/minecraft-server:latest"}, fake.pulled)
	assert.True(t, fake.closeCalled)
}

func TestPullDockerImage_Error(t *testing.T) {
	fake := &fakeImageClient{pullErr: errors.New("拉取超时")}
	withFakeImageClient(t, fake)

	err := PullDockerImage(context.Background(), "x:latest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "拉取超时")
}

func TestRemoveDockerImage(t *testing.T) {
	fake := &fakeImageClient{}
	withFakeImageClient(t, fake)

	require.NoError(t, RemoveDockerImage(context.Background(), "alpine:3.20", true))
	assert.Equal(t, []string{"alpine:3.20"}, fake.removed)
}
