package vlsup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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
)

func fixtureSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fixturePackage(t *testing.T, goos, name string, executable []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		entry, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = entry.Write(executable)
		require.NoError(t, err)
		require.NoError(t, zw.Close())
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(executable)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(executable)
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		require.NoError(t, gz.Close())
	}
	return buf.Bytes()
}

func TestInstallPackageVerifiesBothHashesAndPublishesVersionDirectory(t *testing.T) {
	for _, tc := range []struct{ goos, name string }{
		{"linux", "victoria-logs-prod"},
		{"windows", "victoria-logs-windows-amd64-prod.exe"},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			root := t.TempDir()
			binary := []byte("fixture executable")
			pkg := fixturePackage(t, tc.goos, tc.name, binary)
			spec := packageSpec{tag: "v1-test", goos: tc.goos, goarch: "amd64", filename: tc.name,
				packageSHA: fixtureSHA(pkg), executableSHA: fixtureSHA(binary)}
			path, err := installPackage(context.Background(), bytes.NewReader(pkg), root, spec)
			require.NoError(t, err)
			require.Equal(t, filepath.Join(root, "v1-test", tc.goos+"-amd64", tc.name), path)
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, binary, actual)
			again, err := installPackage(context.Background(), bytes.NewReader(pkg), root, spec)
			require.NoError(t, err)
			require.Equal(t, path, again)

			wrong := spec
			wrong.packageSHA = fixtureSHA([]byte("other package"))
			_, err = installPackage(context.Background(), bytes.NewReader(pkg), root, wrong)
			require.Error(t, err)
			require.Equal(t, binary, actual)
		})
	}
}

func TestInstallPackageRejectsTraversalAndExecutableHashMismatch(t *testing.T) {
	root := t.TempDir()
	binary := []byte("binary")
	pkg := fixturePackage(t, "linux", "../victoria-logs-prod", binary)
	spec := packageSpec{tag: "v1-test", goos: "linux", goarch: "amd64", filename: "victoria-logs-prod",
		packageSHA: fixtureSHA(pkg), executableSHA: fixtureSHA(binary)}
	_, err := installPackage(context.Background(), bytes.NewReader(pkg), root, spec)
	require.ErrorContains(t, err, "unexpected executable archive entry")
	require.NoFileExists(t, filepath.Join(root, "v1-test", "linux-amd64", spec.filename))

	pkg = fixturePackage(t, "linux", spec.filename, binary)
	spec.packageSHA = fixtureSHA(pkg)
	spec.executableSHA = fixtureSHA([]byte("wrong binary"))
	_, err = installPackage(context.Background(), bytes.NewReader(pkg), root, spec)
	require.ErrorContains(t, err, "sha256 mismatch")
	require.NoFileExists(t, filepath.Join(root, "v1-test", "linux-amd64", spec.filename))
}

func TestInstallApprovedPackageRealArchive(t *testing.T) {
	archive := os.Getenv("JIANMANAGER_TEST_VL_ARCHIVE")
	platform := os.Getenv("JIANMANAGER_TEST_VL_PLATFORM")
	if archive == "" || platform == "" {
		t.Skip("set JIANMANAGER_TEST_VL_ARCHIVE and JIANMANAGER_TEST_VL_PLATFORM for approved package integration")
	}
	path, err := InstallApprovedPackage(context.Background(), archive, t.TempDir(), platform, "amd64")
	require.NoError(t, err)
	require.NoError(t, VerifyAsset(path, ApprovedExeSHA256(platform, "amd64")))
}

func TestDownloadPackageRejectsForeignHostAndVerifiesBytes(t *testing.T) {
	pkg := fixturePackage(t, "linux", "victoria-logs-prod", []byte("downloaded binary"))
	spec := packageSpec{tag: "v1-test", goos: "linux", goarch: "amd64", filename: "victoria-logs-prod",
		packageSHA: fixtureSHA(pkg), executableSHA: fixtureSHA([]byte("downloaded binary"))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pkg)
	}))
	defer server.Close()
	url := server.URL + "/log-vl-assets/v1-test/linux/amd64/package?token=private-test-token"
	_, err := downloadPackage(context.Background(), url, t.TempDir(), "not-the-cp", spec)
	require.ErrorContains(t, err, "not an approved endpoint")
	require.NotContains(t, err.Error(), "private-test-token")

	path, err := downloadPackage(context.Background(), url, t.TempDir(), "127.0.0.1", spec)
	require.NoError(t, err)
	require.NoError(t, VerifyAsset(path, spec.executableSHA))
	wrong := spec
	wrong.packageSHA = strings.Repeat("0", 64)
	_, err = downloadPackage(context.Background(), url, t.TempDir(), "127.0.0.1", wrong)
	require.ErrorContains(t, err, "sha256 mismatch")
}
