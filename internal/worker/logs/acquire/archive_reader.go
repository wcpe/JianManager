package acquire

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// 归档摘要与解压续读各只保留一个有界会话，不把整包正文留在内存。
type archiveHashState struct {
	path     string
	size     int64
	mod      int64
	dev, ino uint64
	offset   int64
	digest   hash.Hash
	sum      string
}

type archiveReadSession struct {
	path, objectID string
	file           *os.File
	body           io.ReadCloser
	round          *roundReader
	reader         *bufio.Reader
	offset         uint64
}

// SetReadLimits 为归档摘要、解压跳过和正文读取设置共同单轮字节预算。
func (a *ArchiveImporter) SetReadLimits(limits ReadLimits) error {
	normalized, err := limits.normalized()
	if err != nil {
		return err
	}
	a.readLimits = normalized
	return nil
}

// Close 释放本导入器最多一个在途归档描述符，未消费正文仍在原物上。
func (a *ArchiveImporter) Close() error {
	if a.readSession == nil {
		return nil
	}
	session := a.readSession
	a.readSession = nil
	bodyErr := session.body.Close()
	fileErr := session.file.Close()
	if bodyErr != nil {
		return bodyErr
	}
	return fileErr
}

func (a *ArchiveImporter) hashArchiveBounded(path string, budget int64) (string, int64, int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, 0, false, err
	}
	dev, ino, _ := fileIdentity(info)
	state := a.hashState
	if state == nil || state.path != path || state.size != info.Size() || state.mod != info.ModTime().UnixNano() || state.dev != dev || state.ino != ino {
		_ = a.Close()
		state = &archiveHashState{path: path, size: info.Size(), mod: info.ModTime().UnixNano(), dev: dev, ino: ino, digest: sha256.New()}
		a.hashState = state
	}
	if state.sum != "" {
		return state.sum, state.size, 0, true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, false, err
	}
	defer f.Close()
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return "", 0, 0, false, err
	}
	n, err := io.CopyBuffer(state.digest, io.LimitReader(f, budget), make([]byte, 32<<10))
	state.offset += n
	if err != nil {
		return "", state.size, n, false, err
	}
	if state.offset < state.size {
		if n < budget {
			return "", state.size, n, false, io.ErrUnexpectedEOF
		}
		return "", state.size, n, false, nil
	}
	state.sum = hex.EncodeToString(state.digest.Sum(nil))
	return state.sum, state.size, n, true, nil
}

func (a *ArchiveImporter) recordArchiveReadLimit(err *ReadLimitError) error {
	if entry := a.led.Get(a.key); entry != nil {
		for _, gap := range entry.Gaps {
			if gap.Reason == ReadLineTooLongReason && gap.StartPos == err.LogicalStart && gap.Detail == err.Error() {
				return nil
			}
		}
	}
	return a.led.RecordGap(a.key, err.LogicalStart, err.LogicalStart, ReadLineTooLongReason, err.Error())
}

func (a *ArchiveImporter) archiveSession(path, objectID string, format archiveFormat, target uint64, budget int64) (*archiveReadSession, error) {
	session := a.readSession
	if session != nil && (session.path != path || session.objectID != objectID || session.offset > target) {
		_ = a.Close()
		session = nil
	}
	if session == nil {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		body, err := format.decode(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		round := &roundReader{reader: body, remaining: budget}
		session = &archiveReadSession{path: path, objectID: objectID, file: f, body: body, round: round, reader: bufio.NewReaderSize(round, 32<<10)}
		a.readSession = session
	} else {
		session.round.remaining, session.round.read = budget, 0
	}
	return session, nil
}
