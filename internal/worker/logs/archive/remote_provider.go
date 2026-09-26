package archive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/storage"
)

// RemoteS3Provider is the production S3-compatible archive provider. It uses
// the Worker's existing path-style SigV4 implementation and keeps credentials
// outside the repository/config snapshots.
type RemoteS3Provider struct {
	store  *storage.S3ObjectStore
	kind   string
	prefix string
}

func NewRemoteS3Provider(cfg storage.Config) (*RemoteS3Provider, error) {
	cfg.Type = storage.TypeS3
	store, err := storage.NewS3ObjectStore(cfg)
	if err != nil {
		return nil, err
	}
	return &RemoteS3Provider{store: store, kind: KindS3, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

func NewRemoteMinioProvider(cfg storage.Config) (*RemoteS3Provider, error) {
	cfg.Type = storage.TypeS3
	store, err := storage.NewS3ObjectStore(cfg)
	if err != nil {
		return nil, err
	}
	return &RemoteS3Provider{store: store, kind: KindMinio, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

func (p *RemoteS3Provider) Kind() string { return p.kind }

func (p *RemoteS3Provider) objectKey(key PartitionKey, relPath string) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	clean := path.Clean(strings.ReplaceAll(relPath, "\\", "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("%w: rel_path %q escapes partition", ErrInvalidKey, relPath)
	}
	parts := make([]string, 0, 5)
	if p.prefix != "" {
		parts = append(parts, p.prefix)
	}
	parts = append(parts, key.StorageNamespace, key.UTCDay, fmt.Sprintf("g%d", key.Generation), clean)
	return strings.Join(parts, "/"), nil
}

func (p *RemoteS3Provider) partitionPrefix(key PartitionKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	parts := make([]string, 0, 4)
	if p.prefix != "" {
		parts = append(parts, p.prefix)
	}
	parts = append(parts, key.StorageNamespace, key.UTCDay, fmt.Sprintf("g%d", key.Generation))
	return strings.Join(parts, "/") + "/", nil
}

func (p *RemoteS3Provider) Put(ctx context.Context, key PartitionKey, relPath string, data []byte) error {
	objectKey, err := p.objectKey(key, relPath)
	if err != nil {
		return err
	}
	return p.store.Upload(ctx, objectKey, bytes.NewReader(data), int64(len(data)))
}

func (p *RemoteS3Provider) Get(ctx context.Context, key PartitionKey, relPath string) ([]byte, error) {
	objectKey, err := p.objectKey(key, relPath)
	if err != nil {
		return nil, err
	}
	r, err := p.store.Download(ctx, objectKey)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (p *RemoteS3Provider) Exists(ctx context.Context, key PartitionKey, relPath string) (bool, error) {
	objectKey, err := p.objectKey(key, relPath)
	if err != nil {
		return false, err
	}
	return p.store.Exists(ctx, objectKey)
}

func (p *RemoteS3Provider) List(ctx context.Context, key PartitionKey) ([]string, error) {
	prefix, err := p.partitionPrefix(key)
	if err != nil {
		return nil, err
	}
	keys, err := p.store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, objectKey := range keys {
		if strings.HasPrefix(objectKey, prefix) {
			out = append(out, strings.TrimPrefix(objectKey, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *RemoteS3Provider) Delete(ctx context.Context, key PartitionKey, relPath string) error {
	objectKey, err := p.objectKey(key, relPath)
	if err != nil {
		return err
	}
	return p.store.Delete(ctx, objectKey)
}

var _ Provider = (*RemoteS3Provider)(nil)
