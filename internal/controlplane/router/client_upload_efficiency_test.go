package router

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// buildBatchBody 构造聚合上传 multipart 体：可控 meta JSON 与 files part 序列。
func buildBatchBody(t *testing.T, metaJSON string, files [][]byte, fileField string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if metaJSON != "" {
		if err := mw.WriteField("meta", metaJSON); err != nil {
			t.Fatalf("写 meta part: %v", err)
		}
	}
	for i, f := range files {
		fw, err := mw.CreateFormFile(fileField, "f"+string(rune('0'+i)))
		if err != nil {
			t.Fatalf("建文件 part: %v", err)
		}
		if _, err := fw.Write(f); err != nil {
			t.Fatalf("写文件 part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart: %v", err)
	}
	return &buf, mw.Boundary()
}

func TestReadBatchMetaPart_HappyPath(t *testing.T) {
	body, boundary := buildBatchBody(t, `[{"filename":"a.jar","size":3,"sha256":"`+repeatHex(t)+`"}]`, nil, "files")
	mr := multipart.NewReader(body, boundary)

	metas, err := readBatchMetaPart(mr)
	if err != nil {
		t.Fatalf("readBatchMetaPart: %v", err)
	}
	if len(metas) != 1 || metas[0].Filename != "a.jar" || metas[0].Size != 3 {
		t.Fatalf("meta 解析不符: %+v", metas)
	}
}

func TestReadBatchMetaPart_MetaMustBeFirst(t *testing.T) {
	// 首 part 是 files 而非 meta → 拒。
	body, boundary := buildBatchBody(t, "", [][]byte{[]byte("abc")}, "files")
	mr := multipart.NewReader(body, boundary)

	_, err := readBatchMetaPart(mr)
	if !errors.Is(err, errBatchMultipartInvalid) {
		t.Fatalf("期望 errBatchMultipartInvalid，得 %v", err)
	}
}

func TestReadBatchMetaPart_BadJSON(t *testing.T) {
	body, boundary := buildBatchBody(t, `{not-json`, nil, "files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); !errors.Is(err, errBatchMultipartInvalid) {
		t.Fatalf("期望 errBatchMultipartInvalid，得 %v", err)
	}
}

func TestConsumeBatchFileParts_OrderAndCount(t *testing.T) {
	files := [][]byte{[]byte("one"), []byte("two-2"), []byte("")}
	body, boundary := buildBatchBody(t, `[]`, files, "files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); err != nil {
		t.Fatalf("meta: %v", err)
	}

	var got [][]byte
	err := consumeBatchFileParts(mr, len(files), func(i int, r io.Reader) error {
		data, rerr := io.ReadAll(r)
		if rerr != nil {
			return rerr
		}
		got = append(got, data)
		return nil
	})
	if err != nil {
		t.Fatalf("consumeBatchFileParts: %v", err)
	}
	for i := range files {
		if !bytes.Equal(files[i], got[i]) {
			t.Fatalf("第 %d 个 part 字节不符: %q != %q", i, got[i], files[i])
		}
	}
}

func TestConsumeBatchFileParts_TooFewParts(t *testing.T) {
	body, boundary := buildBatchBody(t, `[]`, [][]byte{[]byte("only-one")}, "files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); err != nil {
		t.Fatalf("meta: %v", err)
	}
	err := consumeBatchFileParts(mr, 2, func(int, io.Reader) error { return nil })
	if !errors.Is(err, errBatchMultipartInvalid) {
		t.Fatalf("part 数不足应拒，得 %v", err)
	}
}

func TestConsumeBatchFileParts_ExtraPartRejected(t *testing.T) {
	body, boundary := buildBatchBody(t, `[]`, [][]byte{[]byte("a"), []byte("b")}, "files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); err != nil {
		t.Fatalf("meta: %v", err)
	}
	err := consumeBatchFileParts(mr, 1, func(int, io.Reader) error { return nil })
	if !errors.Is(err, errBatchMultipartInvalid) {
		t.Fatalf("多余 part 应拒，得 %v", err)
	}
}

func TestConsumeBatchFileParts_WrongFieldRejected(t *testing.T) {
	body, boundary := buildBatchBody(t, `[]`, [][]byte{[]byte("a")}, "not-files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); err != nil {
		t.Fatalf("meta: %v", err)
	}
	err := consumeBatchFileParts(mr, 1, func(int, io.Reader) error { return nil })
	if !errors.Is(err, errBatchMultipartInvalid) {
		t.Fatalf("字段名非 files 应拒，得 %v", err)
	}
}

func TestConsumeBatchFileParts_IngestErrorFailFast(t *testing.T) {
	files := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	body, boundary := buildBatchBody(t, `[]`, files, "files")
	mr := multipart.NewReader(body, boundary)
	if _, err := readBatchMetaPart(mr); err != nil {
		t.Fatalf("meta: %v", err)
	}
	calls := 0
	boom := errors.New("boom")
	err := consumeBatchFileParts(mr, len(files), func(i int, r io.Reader) error {
		calls++
		if i == 1 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("应透传 ingest 错误，得 %v", err)
	}
	if calls != 2 {
		t.Fatalf("fail-fast 应在第 2 个 part 停止，实际调用 %d 次", calls)
	}
}

// repeatHex 返回一个合法 64 位 hex sha256 占位。
func repeatHex(t *testing.T) string {
	t.Helper()
	out := make([]byte, 64)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

// 静态断言：BatchFileMeta 与 service 层保持同名字段（编译期护栏，防 handler/service 漂移）。
var _ = service.BatchFileMeta{Filename: "", Size: 0, SHA256: ""}
