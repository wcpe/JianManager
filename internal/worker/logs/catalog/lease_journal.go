package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// QueryLeaseJournal 的独立修订和文件不参与权威 journal 的 CAS 序号。
type QueryLeaseJournal interface {
	AppendLeaseDelta(LeaseDelta) error
	CompactLeaseDeltas([]LeaseDelta) error
	LeaseDeltaSnapshot() ([]LeaseDelta, error)
}

type leaseDeltaLog struct {
	mu          sync.Mutex
	path        string
	entries     []LeaseDelta
	loaded      bool
	unavailable bool
}

func cloneLeaseDelta(in LeaseDelta) LeaseDelta {
	out := in
	out.InvalidViews = append([]string(nil), in.InvalidViews...)
	out.Keys = append([]PartitionKey(nil), in.Keys...)
	out.Snapshots = append([]QuerySnapshot(nil), in.Snapshots...)
	for i := range out.Snapshots {
		out.Snapshots[i].Projection = nil
	}
	return out
}

func (l *leaseDeltaLog) loadLocked() error {
	if l.unavailable {
		return fmt.Errorf("catalog: custom journal has no durable lease store")
	}
	if l.loaded {
		return nil
	}
	l.loaded = true
	if l.path == "" {
		return nil
	}
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	var bytes int
	for scanner.Scan() {
		bytes += len(scanner.Bytes())
		if bytes > 64<<20 {
			return fmt.Errorf("catalog: lease journal exceeds bounded recovery size")
		}
		var delta LeaseDelta
		if err := json.Unmarshal(scanner.Bytes(), &delta); err != nil {
			return err
		}
		l.entries = append(l.entries, cloneLeaseDelta(delta))
	}
	return scanner.Err()
}
func (l *leaseDeltaLog) snapshot() ([]LeaseDelta, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return nil, err
	}
	out := make([]LeaseDelta, len(l.entries))
	for i, entry := range l.entries {
		out[i] = cloneLeaseDelta(entry)
	}
	return out, nil
}
func (l *leaseDeltaLog) append(delta LeaseDelta) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return err
	}
	delta = cloneLeaseDelta(delta)
	if l.path != "" {
		data, err := json.Marshal(delta)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(append(data, '\n'))
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	l.entries = append(l.entries, delta)
	return nil
}
func (l *leaseDeltaLog) compact(entries []LeaseDelta) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unavailable {
		return fmt.Errorf("catalog: custom journal has no durable lease store")
	}
	next := make([]LeaseDelta, len(entries))
	for i, entry := range entries {
		next[i] = cloneLeaseDelta(entry)
	}
	if l.path != "" {
		dir := filepath.Dir(l.path)
		f, err := os.CreateTemp(dir, ".catalog-leases-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		encoder := json.NewEncoder(f)
		for _, entry := range next {
			if err = encoder.Encode(entry); err != nil {
				_ = f.Close()
				return err
			}
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, l.path); err != nil {
			return err
		}
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	l.entries = next
	l.loaded = true
	return nil
}

func (j *MemJournal) AppendLeaseDelta(delta LeaseDelta) error       { return j.leases.append(delta) }
func (j *MemJournal) CompactLeaseDeltas(entries []LeaseDelta) error { return j.leases.compact(entries) }
func (j *MemJournal) LeaseDeltaSnapshot() ([]LeaseDelta, error)     { return j.leases.snapshot() }
func (j *JournalWithStore) AppendLeaseDelta(delta LeaseDelta) error { return j.leases.append(delta) }
func (j *JournalWithStore) CompactLeaseDeltas(entries []LeaseDelta) error {
	return j.leases.compact(entries)
}
func (j *JournalWithStore) LeaseDeltaSnapshot() ([]LeaseDelta, error) { return j.leases.snapshot() }
