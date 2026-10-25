package eventstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

const maxEventLineBytes = 8 << 20

func addEventMeta(meta *SegmentMeta, event logtypes.Event) {
	if meta.Count == 0 {
		meta.FirstID = event.EventID
		meta.MinEnd = event.Record.End
	}
	meta.Count++
	meta.LastID = event.EventID
	if event.Record.End < meta.MinEnd {
		meta.MinEnd = event.Record.End
	}
	if event.Record.End > meta.MaxEnd {
		meta.MaxEnd = event.Record.End
	}
}

// scanSegment 是校准与快照读取共用的唯一行判据；仅真实文件末尾可丢弃未完成末行。
func scanSegment(reader io.Reader, path string, meta SegmentMeta, allowPartialTail bool, fn func(logtypes.Event) error) (SegmentMeta, error) {
	return scanSegmentContext(context.Background(), reader, path, meta, allowPartialTail, fn)
}

func scanSegmentContext(ctx context.Context, reader io.Reader, path string, meta SegmentMeta, allowPartialTail bool, fn func(logtypes.Event) error) (SegmentMeta, error) {
	actual := SegmentMeta{Seq: meta.Seq, Name: meta.Name, Sealed: meta.Sealed}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return actual, err
		}
		fragment, readErr := buffered.ReadSlice('\n')
		if len(line)+len(fragment) > maxEventLineBytes {
			return actual, fmt.Errorf("%w: line exceeds %d bytes in %s", ErrCorrupt, maxEventLineBytes, path)
		}
		line = append(line, fragment...)
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return actual, fmt.Errorf("%w: read %s: %w", ErrCorrupt, path, readErr)
		}
		if readErr == io.EOF {
			if len(line) != 0 && !allowPartialTail {
				return actual, fmt.Errorf("%w: incomplete line in %s", ErrCorrupt, path)
			}
			return actual, nil
		}
		body := bytes.TrimSpace(line)
		if len(body) != 0 {
			var event logtypes.Event
			if err := json.Unmarshal(body, &event); err != nil {
				return actual, fmt.Errorf("%w: bad complete line in %s: %w", ErrCorrupt, path, err)
			}
			addEventMeta(&actual, event)
			if fn != nil {
				if err := fn(event); err != nil {
					return actual, err
				}
			}
		}
		actual.Bytes += int64(len(line))
		line = line[:0]
	}
}

func calibrateSegment(ctx context.Context, dir string, meta SegmentMeta, last bool) (SegmentMeta, error) {
	path := filepath.Join(dir, meta.Name)
	allowPartialTail := last && !meta.Sealed
	flags := os.O_RDONLY
	if allowPartialTail {
		flags = os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return meta, fmt.Errorf("%w: open segment %s: %w", ErrCorrupt, path, err)
	}
	actual, scanErr := scanSegmentContext(ctx, file, path, meta, allowPartialTail, nil)
	if scanErr != nil {
		return meta, errors.Join(scanErr, file.Close())
	}
	stat, err := file.Stat()
	if err == nil && allowPartialTail && actual.Bytes < stat.Size() {
		// 截断也先耐久，再发布对应清单，避免下次追加粘上崩溃残留半行。
		if err = file.Truncate(actual.Bytes); err == nil {
			err = file.Sync()
		}
	}
	closeErr := file.Close()
	if err != nil {
		return meta, fmt.Errorf("%w: calibrate %s: %w", ErrCorrupt, path, err)
	}
	if closeErr != nil {
		return meta, fmt.Errorf("%w: close %s: %w", ErrCorrupt, path, closeErr)
	}
	return actual, nil
}
