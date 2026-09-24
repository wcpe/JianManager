package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/logasset"
)

func TestApprovedVLAssetStoreRejectsUnapprovedAndCorruptCache(t *testing.T) {
	root, err := dataroot.Resolve(t.TempDir())
	require.NoError(t, err)
	store := NewApprovedVLAssetStore(root)
	payload := []byte("approved test archive")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])
	store.lookup = func(goos, arch string) (logasset.Package, bool) {
		if goos != "linux" || arch != "amd64" {
			return logasset.Package{}, false
		}
		return logasset.Package{OS: goos, Arch: arch, FileName: "approved.tar.gz", PackageSHA256: wantSHA}, true
	}
	_, err = store.Cache(context.Background(), "windows", "arm64", bytes.NewReader(payload))
	require.ErrorContains(t, err, "unapproved")
	_, err = store.Cache(context.Background(), "linux", "amd64", bytes.NewReader([]byte("tampered")))
	require.ErrorContains(t, err, "sha256 mismatch")

	path, err := store.Cache(context.Background(), "linux", "amd64", bytes.NewReader(payload))
	require.NoError(t, err)
	again, err := store.Cache(context.Background(), "linux", "amd64", bytes.NewReader(payload))
	require.NoError(t, err)
	require.Equal(t, path, again)
	f, pkg, err := store.Open("linux", "amd64")
	require.NoError(t, err)
	require.Equal(t, wantSHA, pkg.PackageSHA256)
	f.Close()
	require.NoError(t, os.WriteFile(path, []byte("changed after approval"), 0o600))
	_, _, err = store.Open("linux", "amd64")
	require.ErrorContains(t, err, "sha256 mismatch")
}
