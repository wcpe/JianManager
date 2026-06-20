package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSFTP_Validation(t *testing.T) {
	// 缺 endpoint。
	_, err := New(Config{Type: TypeSFTP, AccessKey: "u"})
	require.Error(t, err)
	// 缺用户名。
	_, err = New(Config{Type: TypeSFTP, Endpoint: "host"})
	require.Error(t, err)
	// 合法：默认补端口 22。
	b, err := New(Config{Type: TypeSFTP, Endpoint: "host", AccessKey: "u", SecretKey: "p"})
	require.NoError(t, err)
	sb, ok := b.(*sftpBackend)
	require.True(t, ok)
	require.Equal(t, "host:22", sb.addr)
}

func TestSFTP_RemotePath(t *testing.T) {
	withPrefix := &sftpBackend{prefix: "/srv/backups"}
	require.Equal(t, "/srv/backups/inst/bk.tar.gz", withPrefix.remotePath("inst/bk.tar.gz"))

	noPrefix := &sftpBackend{prefix: ""}
	require.Equal(t, "inst/bk.tar.gz", noPrefix.remotePath("inst/bk.tar.gz"))
}

func TestShellQuote(t *testing.T) {
	require.Equal(t, "'/srv/backups/a.tar.gz'", shellQuote("/srv/backups/a.tar.gz"))
	// 内部单引号被安全转义，杜绝命令注入。
	require.Equal(t, `'a'\''b'`, shellQuote("a'b"))
	require.NotContains(t, shellQuote("$(rm -rf /)"), "$(rm -rf /)\"") // 仍在单引号内，shell 不展开
}
