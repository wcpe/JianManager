package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/logasset"
)

const maxVLPackageBytes = 128 << 20

// ApprovedVLAssetStore persists only packages from the approved distribution
// manifest. An asset is rehashed before every download, including after cache
// reuse, so a corrupted cache entry never reaches a Worker as approved data.
type ApprovedVLAssetStore struct {
	root   *dataroot.Root
	lookup func(string, string) (logasset.Package, bool)
}

func NewApprovedVLAssetStore(root *dataroot.Root) *ApprovedVLAssetStore {
	return &ApprovedVLAssetStore{root: root, lookup: logasset.Approved}
}

func (s *ApprovedVLAssetStore) packagePath(goos, arch string) (string, logasset.Package, error) {
	if s == nil || s.root == nil {
		return "", logasset.Package{}, fmt.Errorf("log-vl asset store is unavailable")
	}
	pkg, ok := s.lookup(goos, arch)
	if !ok {
		return "", logasset.Package{}, fmt.Errorf("unapproved log-vl platform %s/%s", goos, arch)
	}
	ext := filepath.Ext(pkg.FileName)
	if strings.HasSuffix(pkg.FileName, ".tar.gz") {
		ext = ".tar.gz"
	}
	path := filepath.Join(s.root.ArtifactsDir(), "log-vl", pkg.PackageSHA256[:2], pkg.PackageSHA256+ext)
	return path, pkg, nil
}

func (s *ApprovedVLAssetStore) Cache(ctx context.Context, goos, arch string, reader io.Reader) (string, error) {
	path, pkg, err := s.packagePath(goos, arch)
	if err != nil {
		return "", err
	}
	if reader == nil {
		return "", fmt.Errorf("log-vl asset upload has no body")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".upload-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(reader, maxVLPackageBytes+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", copyErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if n > maxVLPackageBytes {
		return "", fmt.Errorf("log-vl package exceeds byte limit")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != pkg.PackageSHA256 {
		return "", fmt.Errorf("log-vl package sha256 mismatch")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *ApprovedVLAssetStore) Open(goos, arch string) (*os.File, logasset.Package, error) {
	path, pkg, err := s.packagePath(goos, arch)
	if err != nil {
		return nil, pkg, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, pkg, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxVLPackageBytes+1))
	if err == nil && (n > maxVLPackageBytes || hex.EncodeToString(h.Sum(nil)) != pkg.PackageSHA256) {
		err = fmt.Errorf("log-vl cached package sha256 mismatch")
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, pkg, err
	}
	return f, pkg, nil
}
