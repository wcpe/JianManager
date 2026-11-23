//go:build !windows

package grpc

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	psproc "github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/require"
)

type metricsProcessWatch struct {
	metricsProcessRef
}

func watchMetricsProcess(ref metricsProcessRef) (metricsProcessWatch, error) {
	proc, err := psproc.NewProcess(ref.pid)
	if err != nil {
		return metricsProcessWatch{}, err
	}
	created, err := proc.CreateTime()
	if err != nil {
		return metricsProcessWatch{}, err
	}
	if created != ref.createTime {
		return metricsProcessWatch{}, fmt.Errorf("%w: PID %d 创建时间不匹配", errMetricsProcessGone, ref.pid)
	}
	current, err := captureMetricsProcessRef(proc)
	if err != nil {
		if status, statusErr := proc.Status(); statusErr == nil && slices.Contains(status, psproc.Zombie) {
			return metricsProcessWatch{}, errMetricsProcessGone
		}
		return metricsProcessWatch{}, err
	}
	if current.name != ref.name || !sameMetricsExecutable(current.exe, ref.exe) {
		return metricsProcessWatch{}, fmt.Errorf("PID %d 进程身份不匹配", ref.pid)
	}
	return metricsProcessWatch{metricsProcessRef: ref}, nil
}

func (w metricsProcessWatch) exited() (bool, error) {
	exists, err := psproc.PidExists(w.pid)
	if err != nil || !exists {
		return !exists && err == nil, err
	}
	proc, err := psproc.NewProcess(w.pid)
	if err != nil {
		return false, err
	}
	created, err := proc.CreateTime()
	if err != nil || created != w.createTime {
		return created != w.createTime && err == nil, err
	}
	status, err := proc.Status()
	return slices.Contains(status, psproc.Zombie), err
}

func (w metricsProcessWatch) close() {}

func TestMetricsProcessRefMatchesSymlinkedExecutable(t *testing.T) {
	dir := t.TempDir()
	physical := filepath.Join(dir, "metrics java")
	require.NoError(t, os.Mkdir(physical, 0o755))
	alias := filepath.Join(dir, "temp alias")
	require.NoError(t, os.Symlink(physical, alias))
	java := filepath.Join(physical, "java")
	require.NoError(t, os.WriteFile(java, nil, 0o600))
	require.True(t, sameMetricsExecutable(filepath.Join(alias, "java"), java))

	otherJava := filepath.Join(dir, "java")
	require.NoError(t, os.WriteFile(otherJava, nil, 0o600))
	require.False(t, sameMetricsExecutable(filepath.Join(alias, "java"), otherJava))
}
