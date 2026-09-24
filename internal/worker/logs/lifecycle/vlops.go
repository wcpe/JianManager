package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type VLPartitionOps struct {
	Hot, Cold                 *vlsup.Client
	HotDataRoot, ColdDataRoot string
	Catalog                   *catalog.Catalog

	mu        sync.Mutex
	snapshots map[catalog.PartitionKey]string
	verified  map[catalog.PartitionKey]string
}

func NewVLPartitionOps(hot, cold *vlsup.Client, hotRoot, coldRoot string, cat *catalog.Catalog) (*VLPartitionOps, error) {
	if hot == nil || cold == nil || hotRoot == "" || coldRoot == "" || cat == nil {
		return nil, fmt.Errorf("lifecycle: HOT/COLD clients, data roots and Catalog are required")
	}
	return &VLPartitionOps{Hot: hot, Cold: cold, HotDataRoot: hotRoot, ColdDataRoot: coldRoot,
		Catalog: cat, snapshots: make(map[catalog.PartitionKey]string), verified: make(map[catalog.PartitionKey]string)}, nil
}

func (v *VLPartitionOps) Ops() Ops {
	return Ops{
		Drain: v.drain, Snapshot: v.snapshot, Copy: v.copySnapshot, Verify: v.verifyCopy,
		Attach: v.attachCold, LeaseDrain: v.drainLeases, Detach: v.detachHot, Cleanup: v.cleanupHot,
	}
}

func dayName(key catalog.PartitionKey) (string, error) {
	day, err := time.Parse("2006-01-02", key.UTCDay)
	if err != nil {
		return "", err
	}
	return day.Format("20060102"), nil
}

func (v *VLPartitionOps) withTimeout(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx)
}

func (v *VLPartitionOps) drain(catalog.PartitionKey) error {
	return v.withTimeout(func(ctx context.Context) error {
		_, err := v.Hot.Post(ctx, "/internal/force_flush", nil)
		return err
	})
}

func (v *VLPartitionOps) snapshot(key catalog.PartitionKey, _ string) (string, error) {
	day, err := dayName(key)
	if err != nil {
		return "", err
	}
	var paths []string
	err = v.withTimeout(func(ctx context.Context) error {
		body, err := v.Hot.Post(ctx, "/internal/partition/snapshot/create", url.Values{"partition_prefix": {day}})
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &paths)
	})
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("lifecycle: snapshot for %s returned %d paths", day, len(paths))
	}
	if err := requirePathUnder(filepath.Join(v.HotDataRoot, "partitions", day, "snapshots"), paths[0]); err != nil {
		return "", err
	}
	v.mu.Lock()
	v.snapshots[key] = paths[0]
	v.mu.Unlock()
	return paths[0], nil
}

func (v *VLPartitionOps) copySnapshot(key catalog.PartitionKey, snapshotID, _ string) (string, error) {
	day, err := dayName(key)
	if err != nil {
		return "", err
	}
	if snapshotID == "" {
		snapshotID, err = v.snapshot(key, "")
		if err != nil {
			return "", err
		}
	}
	dest := filepath.Join(v.ColdDataRoot, "partitions", day)
	if _, err := os.Stat(dest); err == nil {
		sourceDigest, sourceErr := treeDigest(snapshotID)
		destDigest, verifyErr := treeDigest(dest)
		if sourceErr == nil && verifyErr == nil && sourceDigest == destDigest {
			return dest, nil
		}
		return "", fmt.Errorf("lifecycle: COLD destination already exists with different content")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".staging-"+day+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := copyTree(snapshotID, stage); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (v *VLPartitionOps) verifyCopy(key catalog.PartitionKey, snapshotID, _ string) (string, error) {
	day, err := dayName(key)
	if err != nil {
		return "", err
	}
	if snapshotID == "" {
		v.mu.Lock()
		snapshotID = v.snapshots[key]
		v.mu.Unlock()
	}
	if snapshotID == "" {
		return "", fmt.Errorf("lifecycle: snapshot reference unavailable")
	}
	sourceDigest, err := treeDigest(snapshotID)
	if err != nil {
		return "", err
	}
	destDigest, err := treeDigest(filepath.Join(v.ColdDataRoot, "partitions", day))
	if err != nil {
		return "", err
	}
	if sourceDigest != destDigest {
		return "", fmt.Errorf("lifecycle: staging checksum mismatch")
	}
	v.mu.Lock()
	v.verified[key] = destDigest
	v.mu.Unlock()
	return destDigest, nil
}

func (v *VLPartitionOps) attachCold(key catalog.PartitionKey, _ string) error {
	day, err := dayName(key)
	if err != nil {
		return err
	}
	return v.withTimeout(func(ctx context.Context) error {
		active, err := partitionActive(ctx, v.Cold, day)
		if err != nil || active {
			return err
		}
		_, err = v.Cold.Post(ctx, "/internal/partition/attach", url.Values{"name": {day}})
		return err
	})
}

func (v *VLPartitionOps) drainLeases(key catalog.PartitionKey, oldGeneration uint64) error {
	rec, ok := v.Catalog.Get(key)
	if !ok {
		return fmt.Errorf("lifecycle: Catalog partition disappeared")
	}
	for _, lease := range rec.QueryLeases {
		if lease.Generation == oldGeneration && lease.Active(time.Now().UTC(), oldGeneration) {
			return fmt.Errorf("lifecycle: query lease %s is still active", lease.LeaseID)
		}
	}
	return nil
}

func (v *VLPartitionOps) detachHot(key catalog.PartitionKey, _ string) error {
	day, err := dayName(key)
	if err != nil {
		return err
	}
	return v.withTimeout(func(ctx context.Context) error {
		active, err := partitionActive(ctx, v.Hot, day)
		if err != nil || !active {
			return err
		}
		_, err = v.Hot.Post(ctx, "/internal/partition/detach", url.Values{"name": {day}})
		return err
	})
}

func (v *VLPartitionOps) cleanupHot(key catalog.PartitionKey, _ string) error {
	day, err := dayName(key)
	if err != nil {
		return err
	}
	v.mu.Lock()
	checksum := v.verified[key]
	snapshot := v.snapshots[key]
	v.mu.Unlock()
	if checksum == "" {
		if rec, ok := v.Catalog.Get(key); ok && rec.LastVerifyCompleted {
			checksum = rec.LastVerifyChecksum
		}
	}
	if checksum == "" {
		return fmt.Errorf("lifecycle: target responsibility was not verified")
	}
	hotPartition := filepath.Join(v.HotDataRoot, "partitions", day)
	if err := requirePathUnder(filepath.Join(v.HotDataRoot, "partitions"), hotPartition); err != nil {
		return err
	}
	if err := os.RemoveAll(hotPartition); err != nil {
		return err
	}
	if snapshot != "" {
		_ = v.withTimeout(func(ctx context.Context) error {
			_, err := v.Hot.Post(ctx, "/internal/partition/snapshot/delete", url.Values{"path": {snapshot}})
			return err
		})
	}
	return nil
}

func (v *VLPartitionOps) AllowDetach(key catalog.PartitionKey, _, _ string) (bool, string) {
	v.mu.Lock()
	checksum := v.verified[key]
	v.mu.Unlock()
	if checksum == "" {
		if rec, ok := v.Catalog.Get(key); ok && rec.LastVerifyCompleted {
			checksum = rec.LastVerifyChecksum
		}
	}
	if checksum == "" {
		return false, "verified COLD partition copy required"
	}
	day, err := dayName(key)
	if err != nil {
		return false, err.Error()
	}
	got, err := treeDigest(filepath.Join(v.ColdDataRoot, "partitions", day))
	if err != nil || got != checksum {
		return false, "COLD partition checksum no longer matches the verified receiver"
	}
	return true, "verified COLD partition copy"
}

func partitionActive(ctx context.Context, client *vlsup.Client, day string) (bool, error) {
	body, err := client.Post(ctx, "/internal/partition/list", nil)
	if err != nil {
		return false, err
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return false, err
	}
	for _, name := range names {
		if name == day {
			return true, nil
		}
	}
	return false, nil
}

func requirePathUnder(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("lifecycle: path escapes managed root")
	}
	return nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: snapshot symlink is not allowed")
		}
		if entry.IsDir() {
			return os.MkdirAll(dest, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("lifecycle: unsupported snapshot file type")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeOut, closeIn := out.Close(), in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOut != nil {
			return closeOut
		}
		return closeIn
	})
}

func treeDigest(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: symlink is not allowed")
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(h, filepath.ToSlash(rel))
		_, _ = h.Write([]byte{0})
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
