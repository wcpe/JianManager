package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSixFixNormalizeDoesNotSanitizeValidSplitUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	require.NoError(t, os.WriteFile(path, []byte{'x', 0xe4}, 0600))
	p, err := New(Options{LogSourceID: "six-real-boundary", SourceGeneration: "g1", Path: path, Charset: "utf-8"})
	require.NoError(t, err)
	first, err := p.Poll()
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.Write([]byte{0xb8, 0xad, '\n'})
	require.NoError(t, err)
	require.NoError(t, f.Close())
	second, err := p.Poll()
	require.NoError(t, err)
	all := first
	all = append(all, second...)
	require.Len(t, all, 1)
	require.Equal(t, "x中", all[0].Message)
	require.NotEqual(t, "true", all[0].Fields["encoding_sanitized"])
}
