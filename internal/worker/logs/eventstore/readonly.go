package eventstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// verifyReadOnlyLocked 只建立本次打开的取证视图，不把扫描结果发布为可写清单或修复物理尾部。
func (sec *section) verifyReadOnlyLocked(ctx context.Context) error {
	candidate := cloneManifest(sec.manifest)
	for i, meta := range candidate.Segments {
		actual, err := inspectReadOnlySegment(ctx, sec.dir, meta, i == len(candidate.Segments)-1)
		if err != nil {
			return err
		}
		if actual != meta && (meta.Sealed || i != len(candidate.Segments)-1) {
			return fmt.Errorf("%w: sealed segment metadata mismatch in %s", ErrCorrupt, meta.Name)
		}
		candidate.Segments[i] = actual
	}
	sec.viewMu.Lock()
	sec.committed = candidate
	sec.verified = true
	sec.viewMu.Unlock()
	return nil
}

func inspectReadOnlySegment(ctx context.Context, dir string, meta SegmentMeta, last bool) (SegmentMeta, error) {
	path := filepath.Join(dir, meta.Name)
	file, err := os.Open(path)
	if err != nil {
		return meta, fmt.Errorf("%w: open read-only segment %s: %w", ErrCorrupt, path, err)
	}
	stat, err := file.Stat()
	if err != nil {
		return meta, errors.Join(fmt.Errorf("%w: stat read-only segment %s: %w", ErrCorrupt, path, err), file.Close())
	}
	// 以观察到的文件末端为界，只冻结已完成的行；另一个写者后来补齐半行也不会被截断。
	reader := io.NewSectionReader(file, 0, stat.Size())
	actual, scanErr := scanSegmentContext(ctx, reader, path, meta, last && !meta.Sealed, nil)
	if scanErr == nil {
		position, seekErr := reader.Seek(0, io.SeekCurrent)
		if seekErr != nil {
			scanErr = seekErr
		} else if position != stat.Size() {
			scanErr = fmt.Errorf("%w: read-only segment shortened during capture in %s", ErrCorrupt, path)
		}
	}
	return actual, errors.Join(scanErr, file.Close())
}
