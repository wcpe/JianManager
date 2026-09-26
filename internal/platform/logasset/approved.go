// Package logasset contains the approved VictoriaLogs distribution fingerprint
// shared by the Control Plane cache and the Worker installer.
package logasset

const (
	Tag           = "v1.52.0"
	BuildID       = "20260716-022147-tags-v1.52.0-0-g46a54c9"
	ReleaseCommit = "46a54c9"
	License       = "Apache-2.0"

	LinuxPackageSHA256      = "d14f585144b8d6813f15e11f0041f487e15e10e5f5e5a31be0311367e93d3494"
	WindowsPackageSHA256    = "cec110095b02da7f9ed3946d1defacbfc015b4e8ad767659939ee1bcadbf43e4"
	LinuxExecutableSHA256   = "26941a2f987795dbae465089020b0b592b05718c5f93f4b328b32b429862899b"
	WindowsExecutableSHA256 = "858985a6dd387c841990d904c29fb1464bad7f896b225f8238115d707428accd"
)

type Package struct {
	OS, Arch         string
	FileName         string
	ExecutableName   string
	PackageSHA256    string
	ExecutableSHA256 string
}

func Approved(goos, goarch string) (Package, bool) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return Package{goos, goarch, "victoria-logs-linux-amd64-v1.52.0.tar.gz", "victoria-logs-prod", LinuxPackageSHA256, LinuxExecutableSHA256}, true
	case "windows/amd64":
		return Package{goos, goarch, "victoria-logs-windows-amd64-v1.52.0.zip", "victoria-logs-windows-amd64-prod.exe", WindowsPackageSHA256, WindowsExecutableSHA256}, true
	default:
		return Package{}, false
	}
}
