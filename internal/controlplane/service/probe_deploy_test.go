package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildDeployServerProbeDownloadRequest_OnlyCarriesReference(t *testing.T) {
	req := buildDeployServerProbeDownloadRequest(
		"inst-1",
		"https://cp.example.test/probe-artifacts/7/download?token=short",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"0.2.0",
		"metrics:\n  enabled: true\n",
	)

	assert.Equal(t, "inst-1", req.InstanceUuid)
	assert.Empty(t, req.Jar)
	assert.Empty(t, req.LibrariesZip)
	assert.Equal(t, "0.2.0", req.Version)
	assert.NotEmpty(t, req.DownloadUrl)
	assert.Len(t, req.Sha256, 64)
}
