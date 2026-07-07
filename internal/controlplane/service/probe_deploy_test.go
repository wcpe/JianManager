package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildDeployServerProbeRequest_IncludesLibrariesZip(t *testing.T) {
	jar := []byte("probe-jar")
	libs := []byte("probe-libraries")
	cfg := "metrics:\n  enabled: true\n"

	req := buildDeployServerProbeRequest("inst-1", jar, libs, cfg)

	assert.Equal(t, "inst-1", req.InstanceUuid)
	assert.Equal(t, jar, req.Jar)
	assert.Equal(t, cfg, req.ConfigYaml)
	assert.Equal(t, libs, req.LibrariesZip)
}
