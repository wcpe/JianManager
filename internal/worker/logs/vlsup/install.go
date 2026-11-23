package vlsup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxPackageBytes = 128 << 20
	maxBinaryBytes  = 256 << 20
)

type packageSpec struct {
	tag, goos, goarch string
	packageSHA        string
	executableSHA     string
	filename          string
}

func approvedPackageSpec(goos, goarch string) (packageSpec, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return packageSpec{AssetTag, goos, goarch, AssetPkgSHA256LinuxAMD64, AssetExeSHA256LinuxAMD64, "victoria-logs-prod"}, nil
	case "windows/amd64":
		return packageSpec{AssetTag, goos, goarch, AssetPkgSHA256WindowsAMD64, AssetExeSHA256WindowsAMD64, "victoria-logs-windows-amd64-prod.exe"}, nil
	default:
		return packageSpec{}, fmt.Errorf("vlsup: no approved package for %s/%s", goos, goarch)
	}
}

// InstallApprovedPackage installs the separately distributed official archive
// under a versioned Worker data directory. Both package and executable hashes
// must match the approved platform baseline before the directory is published.
func InstallApprovedPackage(ctx context.Context, archivePath, root, goos, goarch string) (string, error) {
	spec, err := approvedPackageSpec(goos, goarch)
	if err != nil {
		return "", err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("vlsup: open package: %w", err)
	}
	defer f.Close()
	return installPackage(ctx, f, root, spec)
}

// InstallApprovedURL accepts only the configured CP host (HTTP is permitted
// solely on loopback) and never includes the short-lived token in errors.
func InstallApprovedURL(ctx context.Context, packageURL, expectedSHA, root, allowedHost, goos, goarch string) (string, error) {
	spec, err := approvedPackageSpec(goos, goarch)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(expectedSHA, spec.packageSHA) {
		return "", fmt.Errorf("vlsup: CP package SHA is not approved")
	}
	return downloadPackage(ctx, packageURL, root, allowedHost, spec)
}

func downloadPackage(ctx context.Context, packageURL, root, allowedHost string, spec packageSpec) (string, error) {
	u, err := url.Parse(packageURL)
	if err != nil || u.User != nil || u.Hostname() == "" || allowedHost == "" ||
		!(strings.EqualFold(u.Hostname(), allowedHost) || (IsLoopbackHost(u.Hostname()) && IsLoopbackHost(allowedHost))) ||
		(u.Scheme != "https" && !(u.Scheme == "http" && IsLoopbackHost(u.Hostname()))) {
		return "", fmt.Errorf("vlsup: CP package URL is not an approved endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, packageURL, nil)
	if err != nil {
		return "", fmt.Errorf("vlsup: invalid CP package request")
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vlsup: CP package request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vlsup: CP package status %d", resp.StatusCode)
	}
	return installPackage(ctx, resp.Body, root, spec)
}

func installPackage(ctx context.Context, archive io.Reader, root string, spec packageSpec) (string, error) {
	if ctx == nil || archive == nil || root == "" || spec.packageSHA == "" || spec.executableSHA == "" ||
		!validPackagePart(spec.tag) || !validPackagePart(spec.goos) ||
		!validPackagePart(spec.goarch) || !validPackagePart(spec.filename) {
		return "", fmt.Errorf("vlsup: invalid package manifest")
	}
	if err := os.MkdirAll(filepath.Join(root, spec.tag), 0o700); err != nil {
		return "", err
	}
	parent := filepath.Join(root, spec.tag)
	finalDir := filepath.Join(parent, spec.goos+"-"+spec.goarch)
	finalBinary := filepath.Join(finalDir, spec.filename)
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	packagePath := filepath.Join(stage, "package")
	file, err := os.OpenFile(packagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(file, io.LimitReader(&contextReader{ctx, archive}, maxPackageBytes+1))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", fmt.Errorf("vlsup: stage package: %w", copyErr)
	}
	if written > maxPackageBytes {
		return "", fmt.Errorf("vlsup: package exceeds byte limit")
	}
	if err := VerifyAsset(packagePath, spec.packageSHA); err != nil {
		return "", err
	}
	executablePath := filepath.Join(stage, spec.filename)
	if err := unpackExecutable(packagePath, executablePath, spec); err != nil {
		return "", err
	}
	if err := VerifyAsset(executablePath, spec.executableSHA); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := os.Stat(finalDir); err == nil {
		if err := VerifyAsset(finalBinary, spec.executableSHA); err != nil {
			return "", fmt.Errorf("vlsup: existing installation is invalid: %w", err)
		}
		return finalBinary, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Remove(packagePath); err != nil {
		return "", err
	}
	if err := os.Rename(stage, finalDir); err != nil {
		return "", fmt.Errorf("vlsup: publish installation: %w", err)
	}
	return finalBinary, nil
}

func validPackagePart(part string) bool {
	return part != "" && part != "." && part != ".." && !strings.ContainsAny(part, `/\`)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func unpackExecutable(packagePath, output string, spec packageSpec) error {
	if spec.goos == "windows" {
		return unpackZipExecutable(packagePath, output, spec.filename)
	}
	f, err := os.Open(packagePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name != spec.filename || hdr.Typeflag != tar.TypeReg || found || hdr.Size < 0 || hdr.Size > maxBinaryBytes {
			return fmt.Errorf("vlsup: unexpected executable archive entry %q", hdr.Name)
		}
		if err := writeExecutable(output, io.LimitReader(tr, hdr.Size), 0o755); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("vlsup: package executable missing")
	}
	return nil
}

func unpackZipExecutable(packagePath, output, filename string) error {
	zr, err := zip.OpenReader(packagePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != filename || !zr.File[0].Mode().IsRegular() || zr.File[0].UncompressedSize64 > maxBinaryBytes {
		return fmt.Errorf("vlsup: unexpected executable zip entry")
	}
	r, err := zr.File[0].Open()
	if err != nil {
		return err
	}
	defer r.Close()
	return writeExecutable(output, io.LimitReader(r, maxBinaryBytes+1), 0o755)
}

func writeExecutable(path string, reader io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, reader)
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if n > maxBinaryBytes {
		return fmt.Errorf("vlsup: executable exceeds byte limit")
	}
	return copyErr
}
