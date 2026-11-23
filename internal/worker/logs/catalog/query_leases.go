package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var ErrQueryViewStale = errors.New("catalog: query view stale")
var ErrQueryAdmission = errors.New("catalog: query lease admission rejected")

const MaxQueryLeaseTTL = 15 * time.Minute
const MaxQuerySnapshotPartitions = 4096
const MaxQueryLeaseViews = 4096
const MaxQueryLeaseReferences = 65536
const MaxQueryLeaseSnapshotBytes = 128 << 20
const queryCreationTicketTTL = time.Minute

// QuerySnapshotRequest 一次原子选取完整分区集合，TTL 必须有界且非零。
type QuerySnapshotRequest struct {
	ViewID string
	Holder string
	Keys   []PartitionKey
	Now    time.Time
	TTL    time.Duration
}

// QuerySnapshot 固定一个分区的完整已发布投影和物理读取位置。
type QuerySnapshot struct {
	Key        PartitionKey
	Owner      Owner
	DirID      string
	Generation uint64
	Projection *PublishedProjection
	LeaseID    string
	ExpiresAt  time.Time
}

// LeaseDelta 是租约专用增量；不包含可覆盖发布状态的 Record。
type LeaseDelta struct {
	Epoch         string          `json:"epoch,omitempty"`
	Reset         bool            `json:"reset,omitempty"`
	Revision      uint64          `json:"revision"`
	ViewID        string          `json:"view_id"`
	Holder        string          `json:"holder,omitempty"`
	Snapshots     []QuerySnapshot `json:"snapshots,omitempty"`
	Keys          []PartitionKey  `json:"keys,omitempty"`
	InvalidReason string          `json:"invalid_reason,omitempty"`
	InvalidViews  []string        `json:"invalid_views,omitempty"`
	ExpiresAt     time.Time       `json:"expires_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at,omitempty"`
}

type queryLeaseState struct {
	snapshots []QuerySnapshot
	keys      []PartitionKey
	holder    string
	invalid   string
	pins      uint64
	expiresAt time.Time
	createdAt time.Time
	bytes     int
}

func cloneQuerySnapshots(in []QuerySnapshot) []QuerySnapshot {
	out := append([]QuerySnapshot(nil), in...)
	for i := range out {
		out[i].Projection = (&Record{PublishedProjection: in[i].Projection}).Clone().PublishedProjection
	}
	return out
}

func (c *Catalog) preserveLeasesLocked(rec *Record) {
	rec.QueryLeases = nil
	for _, lease := range c.leaseByPartition[rec.Key] {
		rec.QueryLeases = append(rec.QueryLeases, lease)
	}
}

func (c *Catalog) refreshLeaseRefsLocked(viewID string, state *queryLeaseState) {
	for _, snap := range state.snapshots {
		refs := c.leaseByPartition[snap.Key]
		if state.invalid != "" && state.pins == 0 {
			delete(refs, viewID)
			if len(refs) == 0 {
				delete(c.leaseByPartition, snap.Key)
			}
		} else {
			if refs == nil {
				refs = make(map[string]QueryLeaseRef)
				c.leaseByPartition[snap.Key] = refs
			}
			refs[viewID] = QueryLeaseRef{LeaseID: snap.LeaseID, ViewID: viewID, Generation: snap.Generation, Owner: snap.Owner, DirID: snap.DirID, CreatedAt: state.createdAt, ExpiresAt: snap.ExpiresAt, Holder: state.holder, InvalidReason: state.invalid}
		}
		if rec := c.records[snap.Key]; rec != nil {
			c.preserveLeasesLocked(rec)
		}
	}
}

func (c *Catalog) retireViewLocked(viewID string, state *queryLeaseState) {
	if state.invalid == "" || state.pins != 0 {
		return
	}
	delete(c.views, viewID)
	state.snapshots = nil
	state.keys = nil
}

func (c *Catalog) applyLeaseDeltaLocked(delta *LeaseDelta) {
	if delta.Revision > c.leaseRevision {
		c.leaseRevision = delta.Revision
	}
	if len(delta.InvalidViews) > 0 {
		affected := make(map[PartitionKey]bool)
		for _, id := range delta.InvalidViews {
			state := c.views[id]
			if state == nil {
				continue
			}
			state.invalid = delta.InvalidReason
			for _, snap := range state.snapshots {
				affected[snap.Key] = true
				refs := c.leaseByPartition[snap.Key]
				if state.pins == 0 {
					delete(refs, id)
					if len(refs) == 0 {
						delete(c.leaseByPartition, snap.Key)
					}
				} else {
					ref := refs[id]
					ref.InvalidReason = state.invalid
					refs[id] = ref
				}
			}
			c.retireViewLocked(id, state)
		}
		for key := range affected {
			if rec := c.records[key]; rec != nil {
				c.preserveLeasesLocked(rec)
			}
		}
		return
	}
	state := c.views[delta.ViewID]
	if state == nil {
		state = &queryLeaseState{}
		c.views[delta.ViewID] = state
	}
	if !delta.ExpiresAt.IsZero() {
		state.expiresAt = delta.ExpiresAt
		state.createdAt = delta.CreatedAt
	}
	if delta.Snapshots != nil {
		state.snapshots = cloneQuerySnapshots(delta.Snapshots)
		state.keys = append([]PartitionKey(nil), delta.Keys...)
		state.holder = delta.Holder
	}
	if delta.InvalidReason != "" {
		state.invalid = delta.InvalidReason
	}
	c.refreshLeaseRefsLocked(delta.ViewID, state)
	c.retireViewLocked(delta.ViewID, state)
}

func (c *Catalog) initializeQueryLeases() {
	c.views = make(map[string]*queryLeaseState)
	c.leaseByPartition = make(map[PartitionKey]map[string]QueryLeaseRef)
	c.pendingViews = make(map[string]time.Time)
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		c.leaseInitErr = err
		return
	}
	c.viewEpoch = hex.EncodeToString(epoch[:])
	log, ok := c.journal.(QueryLeaseJournal)
	if !ok {
		c.leaseInitErr = fmt.Errorf("%w: dedicated lease persistence unavailable", ErrQueryAdmission)
		return
	}
	c.leaseLog = log
	if entries, err := log.LeaseDeltaSnapshot(); err == nil {
		for _, entry := range entries {
			if entry.Revision > c.leaseRevision {
				c.leaseRevision = entry.Revision
			}
		}
	}
	c.leaseRevision++
	c.leaseInitErr = log.CompactLeaseDeltas([]LeaseDelta{{Epoch: c.viewEpoch, Reset: true, Revision: c.leaseRevision}})
}

func (c *Catalog) compactLeaseLogLocked() error {
	if c.leaseLog == nil || c.viewEpoch == "" {
		return fmt.Errorf("%w: lease persistence unavailable", ErrQueryAdmission)
	}
	entries := []LeaseDelta{{Epoch: c.viewEpoch, Reset: true, Revision: c.leaseRevision}}
	ids := make([]string, 0, len(c.views))
	for id := range c.views {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := c.views[id]
		entries = append(entries, LeaseDelta{Epoch: c.viewEpoch, Revision: c.leaseRevision, ViewID: id, Holder: s.holder, Snapshots: s.snapshots, Keys: s.keys, ExpiresAt: s.expiresAt, CreatedAt: s.createdAt, InvalidReason: s.invalid})
	}
	if err := c.leaseLog.CompactLeaseDeltas(entries); err != nil {
		return fmt.Errorf("%w: %v", ErrQueryAdmission, err)
	}
	c.leaseDirtyOps = 0
	c.leaseDirtyBytes = 0
	c.leaseInitErr = nil
	return nil
}

func (c *Catalog) commitLeaseDeltaLocked(delta *LeaseDelta) error {
	if c.leaseInitErr != nil || c.leaseDirtyOps >= 256 || c.leaseDirtyBytes >= 1<<20 {
		if err := c.compactLeaseLogLocked(); err != nil {
			return err
		}
	}
	if c.leaseLog == nil {
		return fmt.Errorf("%w: dedicated lease persistence unavailable", ErrQueryAdmission)
	}
	delta.Epoch = c.viewEpoch
	delta.Revision = c.leaseRevision + 1
	durable := cloneLeaseDelta(*delta)
	data, err := json.Marshal(durable)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryAdmission, err)
	}
	if err := c.leaseLog.AppendLeaseDelta(durable); err != nil {
		// 同步或关闭失败仍可能已经追加；下次写前必须先按真实active/pin集合压缩。
		c.leaseInitErr = err
		return fmt.Errorf("%w: %v", ErrQueryAdmission, err)
	}
	c.leaseDirtyBytes += len(data)
	c.leaseDirtyOps++
	c.applyLeaseDeltaLocked(delta)
	return nil
}

// NewQueryViewID 签发有界、一次性创建票据；消费后没有票据的旧ID不能再创建。
func (c *Catalog) NewQueryViewID(now time.Time) (string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := c.PruneQueryLeases(now); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leaseInitErr != nil {
		if err := c.compactLeaseLogLocked(); err != nil {
			return "", err
		}
	}
	if c.leaseLog == nil || c.viewEpoch == "" {
		return "", ErrQueryAdmission
	}
	for id, expires := range c.pendingViews {
		if !now.Before(expires) {
			delete(c.pendingViews, id)
		}
	}
	if len(c.pendingViews) >= MaxQueryLeaseViews || len(c.views) >= MaxQueryLeaseViews {
		return "", ErrQueryAdmission
	}
	c.nextView++
	if c.nextView == 0 {
		return "", ErrQueryAdmission
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("%w: random creation nonce unavailable", ErrQueryAdmission)
	}
	id := fmt.Sprintf("%s-%x-%s", c.viewEpoch, c.nextView, hex.EncodeToString(nonce[:]))
	c.pendingViews[id] = now.Add(queryCreationTicketTTL)
	return id, nil
}

// SnapshotAndLease 在同一把锁内选取并登记租约；相同 view 只能重取相同集合。
func (c *Catalog) SnapshotAndLease(req QuerySnapshotRequest) ([]QuerySnapshot, error) {
	if req.ViewID == "" || len(req.ViewID) > 256 || len(req.Holder) > 512 || len(req.Keys) > MaxQuerySnapshotPartitions || req.TTL <= 0 || req.TTL > MaxQueryLeaseTTL {
		return nil, fmt.Errorf("%w: invalid view ID or TTL", ErrQueryViewStale)
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	keys := append([]PartitionKey(nil), req.Keys...)
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.views[req.ViewID]; state != nil {
		if err := c.validateViewLocked(state, req.Now); err != nil {
			return nil, err
		}
		if len(keys) != len(state.keys) {
			return nil, ErrQueryViewStale
		}
		for i, key := range keys {
			if key != state.keys[i] {
				return nil, ErrQueryViewStale
			}
		}
		return cloneQuerySnapshots(state.snapshots), nil
	}
	expires, issued := c.pendingViews[req.ViewID]
	if !issued || !req.Now.Before(expires) {
		delete(c.pendingViews, req.ViewID)
		return nil, ErrQueryViewStale
	}
	refs, bytes := 0, 0
	for _, state := range c.views {
		refs += len(state.keys)
		bytes += state.bytes
	}
	if len(c.views) >= MaxQueryLeaseViews || len(keys) > MaxQueryLeaseReferences-refs {
		return nil, ErrQueryAdmission
	}
	snapshots := make([]QuerySnapshot, 0, len(keys))
	for i, key := range keys {
		if i > 0 && key == keys[i-1] {
			return nil, fmt.Errorf("%w: duplicate partition", ErrQueryViewStale)
		}
		rec := c.records[key]
		if rec == nil || rec.PublishedProjection == nil || !Recover(rec).Queryable || !OwnerForQuery(rec).OK {
			continue
		}
		clone := rec.Clone()
		snapshots = append(snapshots, QuerySnapshot{Key: key, Owner: clone.Owner, DirID: clone.OwnerDirID, Generation: clone.Generation, Projection: clone.PublishedProjection, LeaseID: fmt.Sprintf("%s/%d", req.ViewID, i), ExpiresAt: req.Now.Add(req.TTL)})
	}
	data, err := json.Marshal(struct {
		Snapshots []QuerySnapshot
		Keys      []PartitionKey
	}{snapshots, keys})
	if err != nil {
		return nil, err
	}
	if len(data) > MaxQueryLeaseSnapshotBytes-bytes {
		return nil, ErrQueryAdmission
	}
	if err := c.commitLeaseDeltaLocked(&LeaseDelta{ViewID: req.ViewID, Holder: req.Holder, Snapshots: snapshots, Keys: keys, CreatedAt: req.Now, ExpiresAt: req.Now.Add(req.TTL)}); err != nil {
		return nil, err
	}
	c.views[req.ViewID].bytes = len(data)
	delete(c.pendingViews, req.ViewID)
	return cloneQuerySnapshots(snapshots), nil
}

// comparisonSourceScopes 只把具备明确身份、水位和白名单的旧格式作等价比较，不补造证据。
func comparisonSourceScopes(pp *PublishedProjection) ([]SourceProjection, bool) {
	if pp == nil {
		return nil, false
	}
	if len(pp.SourceProjections) > 0 {
		return pp.SourceProjections, true
	}
	if len(pp.CoveredSourceGenerations) == 0 {
		return nil, false
	}
	generations := pp.ProjectionGenerations
	if len(generations) == 0 && pp.ProjectionGeneration != "" {
		generations = []string{pp.ProjectionGeneration}
	}
	if len(generations) == 0 {
		return nil, false
	}
	scopes := make([]SourceProjection, 0, len(pp.CoveredSourceGenerations))
	for _, ref := range pp.CoveredSourceGenerations {
		if ref.LogSourceID == "" || ref.SourceGeneration == "" {
			return nil, false
		}
		seq, ok := pp.ClosedVisibleSeq[ref.LogSourceID+"/"+ref.SourceGeneration]
		if !ok {
			seq, ok = pp.ClosedVisibleSeq[ref.LogSourceID]
		}
		if !ok && len(pp.CoveredSourceGenerations) == 1 {
			seq, ok = pp.ClosedVisibleSeq["default"]
		}
		if !ok {
			return nil, false
		}
		scopes = append(scopes, SourceProjection{SourceGenerationRef: ref, ProjectionGenerations: generations, ClosedVisibleSeq: seq})
	}
	return scopes, true
}

func projectionContains(current, fixed *PublishedProjection, migration bool) bool {
	if current == nil || fixed == nil {
		return false
	}
	currentScopes, currentKnown := comparisonSourceScopes(current)
	fixedScopes, fixedKnown := comparisonSourceScopes(fixed)
	if currentKnown != fixedKnown {
		return false
	}
	if currentKnown && len(currentScopes) != len(fixedScopes) {
		return false
	}
	if fixedKnown {
		scopes := make(map[SourceGenerationRef]SourceProjection, len(currentScopes))
		for _, s := range currentScopes {
			scopes[s.SourceGenerationRef] = s
		}
		for _, s := range fixedScopes {
			next, ok := scopes[s.SourceGenerationRef]
			if !ok || next.ClosedVisibleSeq < s.ClosedVisibleSeq {
				return false
			}
			if !migration {
				for _, g := range s.ProjectionGenerations {
					found := false
					for _, ng := range next.ProjectionGenerations {
						found = found || ng == g
					}
					if !found {
						return false
					}
				}
			}
		}
		return true
	}
	if len(current.CoveredSourceGenerations) != len(fixed.CoveredSourceGenerations) {
		return false
	}
	for _, source := range fixed.CoveredSourceGenerations {
		found := false
		for _, next := range current.CoveredSourceGenerations {
			found = found || source == next
		}
		if !found {
			return false
		}
	}
	for key, seq := range fixed.ClosedVisibleSeq {
		if current.ClosedVisibleSeq[key] < seq {
			return false
		}
	}
	if migration {
		return true
	}
	currentGens := current.ProjectionGenerations
	if len(currentGens) == 0 && current.ProjectionGeneration != "" {
		currentGens = []string{current.ProjectionGeneration}
	}
	fixedGens := fixed.ProjectionGenerations
	if len(fixedGens) == 0 && fixed.ProjectionGeneration != "" {
		fixedGens = []string{fixed.ProjectionGeneration}
	}
	for _, g := range fixedGens {
		found := false
		for _, ng := range currentGens {
			found = found || ng == g
		}
		if !found {
			return false
		}
	}
	return true
}

func (c *Catalog) validateViewLocked(state *queryLeaseState, now time.Time) error {
	if state.invalid != "" {
		return fmt.Errorf("%w: %s", ErrQueryViewStale, state.invalid)
	}
	if state.expiresAt.IsZero() || !now.Before(state.expiresAt) {
		return fmt.Errorf("%w: expired", ErrQueryViewStale)
	}
	for _, snap := range state.snapshots {
		if !now.Before(snap.ExpiresAt) {
			return fmt.Errorf("%w: expired", ErrQueryViewStale)
		}
		rec := c.records[snap.Key]
		if rec == nil {
			return ErrQueryViewStale
		}
		same := rec.Owner == snap.Owner && rec.OwnerDirID == snap.DirID && rec.Generation == snap.Generation
		migration := !same && rec.MigrationFromOwner == snap.Owner && rec.MigrationFromDirID == snap.DirID && rec.MigrationFromGeneration == snap.Generation && rec.LastVerifyCompleted && rec.MigrationState != StateDetached && rec.MigrationState != StateCleaned
		if !same && !migration {
			return ErrQueryViewStale
		}
		if !projectionContains(rec.PublishedProjection, snap.Projection, migration) {
			return ErrQueryViewStale
		}
	}
	return nil
}

// PinLeasedRead 保护实际读取至 release，失效或到期仅阻止后续请求。
func (c *Catalog) PinLeasedRead(viewID string, now time.Time) (func(), error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c.mu.Lock()
	state := c.views[viewID]
	if state == nil {
		c.mu.Unlock()
		return nil, ErrQueryViewStale
	}
	if err := c.validateViewLocked(state, now); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	state.pins++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			state.pins--
			c.refreshLeaseRefsLocked(viewID, state)
			c.retireViewLocked(viewID, state)
		})
	}, nil
}

// ReleaseQueryView 撤销未消费票据并持久失效视图；已有请求的 pin 继续保护旧物理位置。
func (c *Catalog) ReleaseQueryView(viewID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pendingViews, viewID)
	state := c.views[viewID]
	if state == nil || state.invalid != "" {
		return nil
	}
	return c.commitLeaseDeltaLocked(&LeaseDelta{ViewID: viewID, InvalidReason: "released"})
}

// PruneQueryLeases 到期后持久禁止新读，但不提前释放在途读取。
func (c *Catalog) PruneQueryLeases(now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, expires := range c.pendingViews {
		if !now.Before(expires) {
			delete(c.pendingViews, id)
		}
	}
	var expiredIDs []string
	for viewID, state := range c.views {
		if state.invalid == "" && (state.expiresAt.IsZero() || !now.Before(state.expiresAt)) {
			expiredIDs = append(expiredIDs, viewID)
		}
	}
	if len(expiredIDs) == 0 {
		return nil
	}
	sort.Strings(expiredIDs)
	return c.commitLeaseDeltaLocked(&LeaseDelta{InvalidViews: expiredIDs, InvalidReason: "expired"})
}
