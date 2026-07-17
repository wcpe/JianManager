package blobstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

func newLocalStore(t *testing.T) (Store, *dataroot.Root) {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	st, err := NewLocal(root)
	require.NoError(t, err)
	return st, root
}

// TestLocalStore_PutFileRename PutFile 以 os.Rename 原子落位（源文件消失、目标就位），
// 与既有 Ingest 的 CAS 落盘行为等价。
func TestLocalStore_PutFileRename(t *testing.T) {
	store, root := newLocalStore(t)
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "ingest.part")
	content := []byte("local rename semantics")
	require.NoError(t, os.WriteFile(src, content, 0o644))

	key := "var/artifacts/client-file/ab/abcd1234.zip"
	require.NoError(t, store.PutFile(ctx, key, src, int64(len(content))))

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "源临时文件应被 rename 走")
	got, err := os.ReadFile(root.Abs(key))
	require.NoError(t, err)
	require.Equal(t, content, got)
}

// TestLocalStore_OpenStatDeleteList Open/Stat/Delete/List 与 Presign 不支持。
func TestLocalStore_OpenStatDeleteList(t *testing.T) {
	store, root := newLocalStore(t)
	ctx := context.Background()

	key := "var/artifacts/client-file/cd/cdef5678.bin"
	content := []byte("blob body")
	require.NoError(t, os.MkdirAll(filepath.Dir(root.Abs(key)), 0o755))
	require.NoError(t, os.WriteFile(root.Abs(key), content, 0o644))

	rc, err := store.Open(ctx, key)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, content, got)

	info, err := store.Stat(ctx, key)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), info.Size)

	list, err := store.List(ctx, "var/artifacts/client-file", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, key, list[0].Key, "登记键统一「/」分隔")

	_, err = store.Presign(key, 0)
	require.ErrorIs(t, err, ErrPresignUnsupported)

	require.NoError(t, store.Delete(ctx, key))
	require.NoError(t, store.Delete(ctx, key), "重复删除应幂等")
	_, err = store.Open(ctx, key)
	require.ErrorIs(t, err, ErrBlobNotFound)
	_, err = store.Stat(ctx, key)
	require.ErrorIs(t, err, ErrBlobNotFound)
}

// TestLocalStore_ListPage_Cursor ListPage 以末键游标分页（FR-349）：跨页全量、字典序、无重复。
func TestLocalStore_ListPage_Cursor(t *testing.T) {
	store, root := newLocalStore(t)
	want := []string{
		"var/artifacts/client-file/aa/1.zip",
		"var/artifacts/client-file/bb/2.zip",
		"var/artifacts/client-file/cc/3.zip",
		"var/artifacts/client-file/dd/4.zip",
		"var/artifacts/client-file/ee/5.zip",
	}
	for _, k := range want {
		require.NoError(t, os.MkdirAll(filepath.Dir(root.Abs(k)), 0o755))
		require.NoError(t, os.WriteFile(root.Abs(k), []byte("x"), 0o644))
	}

	var got []string
	token := ""
	pages := 0
	for {
		items, next, err := store.ListPage(context.Background(), "var/artifacts/client-file", 2, token)
		require.NoError(t, err)
		pages++
		for _, it := range items {
			got = append(got, it.Key)
		}
		if next == "" {
			break
		}
		token = next
	}
	require.Equal(t, want, got, "跨页拼出完整字典序清单")
	require.Equal(t, 3, pages)
}

// TestLocalStore_ListMissingDir 列举不存在目录返回空而非错误（探测/对账容错）。
func TestLocalStore_ListMissingDir(t *testing.T) {
	store, _ := newLocalStore(t)
	out, err := store.List(context.Background(), "var/artifacts/no-such", 10)
	require.NoError(t, err)
	require.Empty(t, out)
}
