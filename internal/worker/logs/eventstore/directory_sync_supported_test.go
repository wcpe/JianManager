//go:build !windows

package eventstore

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectorySyncPropagatesRealClosedHandleFailure(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, directory.Close())
	require.ErrorIs(t, syncDirectoryHandle(directory), os.ErrClosed)
}
