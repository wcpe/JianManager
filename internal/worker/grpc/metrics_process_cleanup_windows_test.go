//go:build windows

package grpc

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type metricsProcessWatch struct {
	pid    int32
	handle windows.Handle
}

func watchMetricsProcess(ref metricsProcessRef) (metricsProcessWatch, error) {
	// 只读句柄：同一内核对象完成身份比对和退出确认，无权终止进程。
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE
	handle, err := windows.OpenProcess(access, false, uint32(ref.pid))
	if err != nil {
		return metricsProcessWatch{}, err
	}
	watch := metricsProcessWatch{pid: ref.pid, handle: handle}
	if err := watch.checkIdentity(ref); err != nil {
		watch.close()
		return metricsProcessWatch{}, err
	}
	return watch, nil
}

func (w metricsProcessWatch) checkIdentity(ref metricsProcessRef) error {
	var creationTime, exitTime, kernelTime, userTime windows.Filetime
	if err := windows.GetProcessTimes(w.handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return err
	}
	if got := creationTime.Nanoseconds() / int64(time.Millisecond); got != ref.createTime {
		return fmt.Errorf("%w: PID %d 创建时间不匹配: got=%d want=%d", errMetricsProcessGone, ref.pid, got, ref.createTime)
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(w.handle, 0, &buffer[0], &size); err != nil {
		return err
	}
	exe := windows.UTF16ToString(buffer[:size])
	if !sameMetricsExecutable(exe, ref.exe) || !strings.EqualFold(filepath.Base(exe), ref.name) {
		return fmt.Errorf("PID %d 进程身份不匹配: exe=%q name=%q", ref.pid, exe, ref.name)
	}
	return nil
}

func (w metricsProcessWatch) exited() (bool, error) {
	state, err := windows.WaitForSingleObject(w.handle, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("未知的进程等待状态: %d", state)
	}
}

func (w metricsProcessWatch) close() {
	_ = windows.CloseHandle(w.handle)
}
