package eventstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectorySyncKeepsPathAndTypeFailuresExplicit(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("fixture"), 0o600))
	require.Error(t, syncDir(file), "目录能力受限不能掩盖传入了普通文件的真实类型错误")
	require.ErrorIs(t, syncDir(filepath.Join(root, "missing")), os.ErrNotExist)
	require.NoError(t, syncDir(root))
}
