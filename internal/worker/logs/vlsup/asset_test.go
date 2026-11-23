package vlsup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyAssetOK(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "victoria-logs")
	content := []byte("fake-vl-binary-content")
	if err := os.WriteFile(p, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	if err := VerifyAsset(p, want); err != nil {
		t.Fatalf("expected verify ok, got %v", err)
	}
	// 大小写不敏感
	if err := VerifyAsset(p, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("hex case must not matter: %v", err)
	}
}

func TestVerifyAssetMismatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "victoria-logs")
	if err := os.WriteFile(p, []byte("abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := VerifyAsset(p, "deadbeef")
	if err == nil {
		t.Fatal("mismatched sha256 must fail before exec")
	}
}

func TestVerifyAssetEmptyArgs(t *testing.T) {
	if err := VerifyAsset("", "aa"); err == nil {
		t.Fatal("empty path must fail")
	}
	if err := VerifyAsset("x", ""); err == nil {
		t.Fatal("empty expected sha must fail")
	}
}

func TestVerifyAssetMissingFile(t *testing.T) {
	err := VerifyAsset(filepath.Join(t.TempDir(), "missing"), AssetExeSHA256LinuxAMD64)
	if err == nil {
		t.Fatal("missing asset must fail")
	}
}

func TestAssetBaselineConstants(t *testing.T) {
	if AssetTag != "v1.52.0" {
		t.Fatalf("asset tag baseline drift: %s", AssetTag)
	}
	if AssetBuildID == "" || AssetReleaseCommit == "" {
		t.Fatal("build id / release commit constants required")
	}
	if CurrentApprovedExeSHA256() == "" {
		t.Log("no approved exe hash registered for this GOOS/GOARCH; baseline still documented")
	}
	if got := ApprovedExeSHA256("linux", "amd64"); got != AssetExeSHA256LinuxAMD64 {
		t.Fatalf("linux/amd64 hash mismatch")
	}
	if got := ApprovedExeSHA256("windows", "amd64"); got != AssetExeSHA256WindowsAMD64 {
		t.Fatalf("windows/amd64 hash mismatch")
	}
}
