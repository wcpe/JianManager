package vlsup

import (
	"errors"
	"fmt"
	"strings"
)

// safeError 在错误跨出supervisor边界前去除受信配置秘密，不保存完整命令行。
func (s *Supervisor) safeError(err error) error {
	if err == nil {
		return nil
	}
	clean := redact(err.Error(), credentialSecrets(s.authUser, s.authPass))
	if clean == err.Error() {
		return err
	}
	return errors.New(clean)
}

// syncProcessStatusLocked 只读取可选快照；锁序supervisor.mu→process.mu→diagnostics.mu，无IO。
func (s *Supervisor) syncProcessStatusLocked(inst *instance) {
	if inst == nil || inst.proc == nil {
		return
	}
	if h, ok := inst.proc.(ExecHandle); ok {
		v := h.Snapshot()
		inst.status.StartedAt = v.StartedAt
		inst.status.ExitedAt = v.ExitedAt
		inst.status.ExitCode = v.ExitCode
		inst.status.ExitCodeKnown = v.ExitCodeKnown
	} else {
		inst.status.StartedAt = inst.startedAt
	}
}

func processExitDetail(proc Process) string {
	h, ok := proc.(ExecHandle)
	if !ok {
		return ""
	}
	v := h.Snapshot()
	detail := ""
	if v.ExitCodeKnown {
		detail = fmt.Sprintf("; exit_code=%d", v.ExitCode)
	}
	if len(v.Diagnostics) > 0 {
		detail += "; diagnostic=" + strings.Join(v.Diagnostics, " | ")
	}
	if len(detail) > 2048 {
		detail = "; 诊断过长，见独立有界诊断落点"
	}
	return detail
}
