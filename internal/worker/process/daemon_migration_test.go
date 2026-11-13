package process

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

func TestMigratedDaemonSocketFallbackRequiresStrongOwnership(t *testing.T) {
	for _, tc := range []struct {
		name         string
		recordUUID   string
		wrapperAlive bool
		wrapperOwned bool
		javaOwned    bool
		wantFallback bool
	}{
		{"确属本实例", "migrated", true, true, true, true},
		{"记录属于其它实例", "other", true, true, true, false},
		{"wrapper已消失", "migrated", false, true, true, false},
		{"wrapper归属不明", "migrated", true, false, true, false},
		{"Java归属不明", "migrated", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			uuid := "migrated"
			oldAddr := filepath.Join(t.TempDir(), uuid+".sock")
			canonical := daemon.SocketAddr(m.pidDir, uuid)
			require.NoError(t, daemon.NewPIDFile(daemon.PIDFileName(m.pidDir, uuid)).WriteRecord(daemon.PIDRecord{
				InstanceUUID: tc.recordUUID, WrapperPID: testWrapperPID, JavaPID: testJavaPID, SocketAddr: oldAddr, WorkDir: "/deleted/workdir",
			}))
			m.recoverPIDAlive = func(pid int) bool { return pid != testWrapperPID || tc.wrapperAlive }
			m.adoptVerifyOwner = func(pid int, id, workDir string, wrapper bool) (bool, string) {
				require.Equal(t, uuid, id)
				require.Equal(t, "/deleted/workdir", workDir)
				if wrapper {
					require.Equal(t, testWrapperPID, pid)
					return tc.wrapperOwned, "test"
				}
				require.Equal(t, testJavaPID, pid)
				return tc.javaOwned, "test"
			}
			var dials []string
			m.recoverDial = func(_ *daemonStrategy, addr string) error {
				dials = append(dials, addr)
				if addr == canonical {
					return nil
				}
				return errors.New("old socket missing")
			}
			m.recoverKillTree = func(int) error { t.Fatal("重定位绝不能杀进程"); return nil }
			err := m.dialWrapper(newDaemonStrategy(m, CommandSpec{UUID: uuid}), oldAddr)
			if tc.wantFallback {
				require.NoError(t, err)
				require.Equal(t, []string{oldAddr, canonical}, dials)
			} else {
				require.Error(t, err)
				require.Equal(t, []string{oldAddr}, dials, "强归属未确认时不可接管当前路径的socket")
			}
		})
	}
}
