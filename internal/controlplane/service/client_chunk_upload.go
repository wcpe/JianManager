package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

var (
	// ErrUploadNotFound 上传会话不存在（uploadId 非法/已完成/已弃单/TTL 过期）。
	ErrUploadNotFound = errors.New("上传会话不存在")
	// ErrUploadChannelMismatch 上传会话不属于该频道（防跨频道操作他人会话）。
	ErrUploadChannelMismatch = errors.New("上传会话与频道不匹配")
	// ErrInvalidChunkIndex 分片序号越界（不在 [0, chunkCount) 内）。
	ErrInvalidChunkIndex = errors.New("分片序号越界")
	// ErrInvalidChunkSize 分片字节数不符（非末片须恰为 chunkSize；末片须为末段应有字节数）。
	ErrInvalidChunkSize = errors.New("分片字节数不符")
	// ErrUploadIncomplete complete 时分片不齐全（缺片）。
	ErrUploadIncomplete = errors.New("分片不齐全")
	// ErrInvalidUploadInit init 参数非法（totalSize<0；0 为空文件、合法）。
	ErrInvalidUploadInit = errors.New("上传初始化参数非法")
)

// 分块上传协议常量（FR-251，增强 FR-088 单次上传）。
const (
	// defaultChunkSize 默认分片大小 8 MiB：远低于常见反代 body 上限，单请求不超时；
	// 4 GiB 文件 ≈ 512 片，会话/HTTP 开销可接受。init 未指定 chunkSize 时用它。
	defaultChunkSize int64 = 8 << 20 // 8 MiB
	// minChunkSize 允许的最小分片（1 MiB）：过小会放大请求数与元数据开销。
	minChunkSize int64 = 1 << 20 // 1 MiB
	// maxChunkSize 允许的最大分片（64 MiB）：上限护栏，避免单请求过大重蹈单次上传短板。
	maxChunkSize int64 = 64 << 20 // 64 MiB
	// uploadSessionTTL 会话空闲存活上限：超过即视为弃单，后台清理删临时分片 + 移除会话。
	uploadSessionTTL = time.Hour
	// uploadSweepInterval 后台清理巡检间隔。
	uploadSweepInterval = 5 * time.Minute
)

// uploadSession 单个分块上传会话的内存态（受 ChunkedUploadService.mu 守护）。
// 会话为内存态：CP 重启即丢失，前端据 init 重开新会话整传（spec §6 待定）。
type uploadSession struct {
	// id 上传会话标识（crypto/rand 生成 hex），也是临时分片子目录名。
	id string
	// channelID 发起上传的频道；chunk/complete/abort 校验频道一致，防跨频道操作。
	channelID string
	// filename 原始文件名（决定 CAS 扩展名/下载名），透传给 PublishFile。
	filename string
	// totalSize 声明的文件总字节数；complete 校验拼装后总字节须匹配。
	totalSize int64
	// chunkSize 每片字节数（末片除外）。
	chunkSize int64
	// chunkCount 分片总数 = ceil(totalSize / chunkSize)。
	chunkCount int64
	// received 已成功落盘的分片序号集合（幂等：重传同 index 覆盖并保持存在）。
	received map[int64]struct{}
	// tempDir 该会话临时分片目录 <cache>/client-uploads/<id>/。
	tempDir string
	// createdAt 会话创建时间。
	createdAt time.Time
	// lastActivity 最近一次 init/chunk 活动时间；TTL 以此计空闲。
	lastActivity time.Time
}

// ChunkedUploadService 客户端分发大文件分块上传服务（FR-251，增强 FR-088）。
//
// 协议：init（建会话）→ 顺序/乱序 PUT 各分片（幂等，失败可重传）→ complete（校验齐全 +
// 总字节 → io.MultiReader 顺序拼装喂 ClientVersionService.PublishFile 落 CAS）→ 返回与单次
// 上传完全一致的 ClientFileResult{sha256,md5,size,codec}。abort/TTL 清理临时分片。
//
// 落盘复用现有 CAS（AssetService.Ingest 经 PublishFile），不额外落一份整文件；临时分片进
// <dataRoot>/cache/client-uploads/<uploadId>/<index>.part。会话为内存态（mutex 守护），
// CP 重启即弃单。加性协议，不推翻既有决策（无新 ADR）。
type ChunkedUploadService struct {
	root     *dataroot.Root
	versions *ClientVersionService
	mu       sync.Mutex
	sessions map[string]*uploadSession
	// now 时间源（可注入便于测试 TTL）；nil 时用 time.Now。
	now func() time.Time
	// stop 停止后台清理 goroutine。
	stop chan struct{}
	// stopped 保证 Stop 幂等。
	stopped bool
}

// NewChunkedUploadService 创建分块上传服务。root 提供 cache 临时区，versions 复用其 PublishFile 落 CAS。
func NewChunkedUploadService(root *dataroot.Root, versions *ClientVersionService) *ChunkedUploadService {
	return &ChunkedUploadService{
		root:     root,
		versions: versions,
		sessions: make(map[string]*uploadSession),
		stop:     make(chan struct{}),
	}
}

// uploadsRoot 返回分块临时区根 <cache>/client-uploads。
func (s *ChunkedUploadService) uploadsRoot() string {
	return filepath.Join(s.root.CacheDir(), "client-uploads")
}

// nowTime 返回当前时间（可注入）。
func (s *ChunkedUploadService) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Start 启动后台 TTL 清理，并清空启动前遗留的临时分片目录（CP 重启会话丢失，残留即弃单）。
// 幂等前提：由装配处调用一次。
func (s *ChunkedUploadService) Start() {
	// 启动即清残留 client-uploads/（上次进程的会话已随重启丢失，物理分片无主）。
	_ = os.RemoveAll(s.uploadsRoot())
	go s.sweepLoop()
}

// Stop 停止后台清理 goroutine（幂等）。
func (s *ChunkedUploadService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stop)
}

// sweepLoop 周期性回收空闲超 TTL 的会话（删临时分片 + 移除会话）。
func (s *ChunkedUploadService) sweepLoop() {
	ticker := time.NewTicker(uploadSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.SweepExpired()
		}
	}
}

// SweepExpired 立即回收所有空闲超 TTL 的会话（导出便于测试）。返回回收的会话数。
func (s *ChunkedUploadService) SweepExpired() int {
	cutoff := s.nowTime().Add(-uploadSessionTTL)
	s.mu.Lock()
	var stale []*uploadSession
	for id, sess := range s.sessions {
		if sess.lastActivity.Before(cutoff) {
			stale = append(stale, sess)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
	for _, sess := range stale {
		_ = os.RemoveAll(sess.tempDir)
	}
	return len(stale)
}

// InitParams init 参数。
type InitParams struct {
	// Filename 原始文件名（决定 CAS 扩展名/下载名），可空。
	Filename string
	// TotalSize 文件总字节数（必，>=0；0=空文件如 .gitkeep，chunkCount=0 无片直达 complete）。
	TotalSize int64
	// ChunkSize 期望分片大小（可空，<=0 用默认；越界则夹取到 [min,max]）。
	ChunkSize int64
}

// InitResult init 返回：上传会话标识 + 服务端敲定的分片大小/片数（前端据此切片）。
type InitResult struct {
	// UploadID 上传会话标识（后续 chunk/complete/abort 用）。
	UploadID string `json:"uploadId"`
	// ChunkSize 服务端敲定的分片大小（前端须遵从）。
	ChunkSize int64 `json:"chunkSize"`
	// ChunkCount 分片总数 = ceil(totalSize / chunkSize)。
	ChunkCount int64 `json:"chunkCount"`
}

// Init 建立分块上传会话：校验参数 → 敲定 chunkSize/chunkCount → 建临时目录 → 登记会话。
// totalSize<0 返回 ErrInvalidUploadInit；totalSize=0 合法（整合包常见 .gitkeep/空配置，BUG 修复：
// 原 <=0 一刀切使含空文件的发布整批弃单），此时 chunkCount=0、无片可传、complete 直接落空内容。
// chunkSize 越界自动夹取到 [minChunkSize,maxChunkSize]。
func (s *ChunkedUploadService) Init(channelID string, p InitParams) (*InitResult, error) {
	if p.TotalSize < 0 {
		return nil, fmt.Errorf("%w: totalSize 不能为负", ErrInvalidUploadInit)
	}
	chunkSize := clampChunkSize(p.ChunkSize)
	// ceil 除法，全程 int64 无 32 位截断；chunkSize 经夹取恒 >=minChunkSize 无除零，totalSize=0 时自然得 0 片。
	chunkCount := (p.TotalSize + chunkSize - 1) / chunkSize

	id, err := newUploadID()
	if err != nil {
		return nil, fmt.Errorf("生成上传会话标识失败: %w", err)
	}
	tempDir := filepath.Join(s.uploadsRoot(), id)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建上传临时目录失败: %w", err)
	}

	now := s.nowTime()
	sess := &uploadSession{
		id:           id,
		channelID:    channelID,
		filename:     p.Filename,
		totalSize:    p.TotalSize,
		chunkSize:    chunkSize,
		chunkCount:   chunkCount,
		received:     make(map[int64]struct{}, chunkCount),
		tempDir:      tempDir,
		createdAt:    now,
		lastActivity: now,
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	return &InitResult{UploadID: id, ChunkSize: chunkSize, ChunkCount: chunkCount}, nil
}

// WriteChunkResult chunk 返回：已收片数 / 总片数（前端可据此展示或校验）。
type WriteChunkResult struct {
	// Received 已成功落盘的分片数。
	Received int64 `json:"received"`
	// Total 分片总数。
	Total int64 `json:"total"`
}

// WriteChunk 写入一个分片（原始字节流）。幂等：重传同 index 覆盖，支持失败重试。
//
// 校验：会话存在且频道匹配、index 在 [0,chunkCount) 内、字节数匹配（非末片须恰为 chunkSize，
// 末片须为末段应有字节数）。分片先落临时文件再原子 rename，避免半写脏片被 complete 采纳。
// 频道不匹配返回 ErrUploadChannelMismatch；越界 ErrInvalidChunkIndex；字节不符 ErrInvalidChunkSize。
func (s *ChunkedUploadService) WriteChunk(channelID, uploadID string, index int64, r io.Reader) (*WriteChunkResult, error) {
	s.mu.Lock()
	sess, ok := s.sessions[uploadID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrUploadNotFound
	}
	if sess.channelID != channelID {
		return nil, ErrUploadChannelMismatch
	}
	if index < 0 || index >= sess.chunkCount {
		return nil, ErrInvalidChunkIndex
	}
	wantSize := s.expectedChunkSize(sess, index)

	// 先落临时文件再原子 rename：写失败/字节不符时目标 part 不被污染（幂等重传仍见旧齐全片）。
	partPath := filepath.Join(sess.tempDir, fmt.Sprintf("%d.part", index))
	tmp, err := os.CreateTemp(sess.tempDir, fmt.Sprintf("%d-*.tmp", index))
	if err != nil {
		return nil, fmt.Errorf("创建分片临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	// 只收 wantSize+1 字节：正好 wantSize 合法；多出 1 字节即判定过大（防超量写盘）。
	written, err := io.Copy(tmp, io.LimitReader(r, wantSize+1))
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("写入分片失败: %w", err)
	}
	if written != wantSize {
		return nil, fmt.Errorf("%w: 分片 %d 期望 %d 字节，实际 %d", ErrInvalidChunkSize, index, wantSize, written)
	}
	if err := os.Rename(tmpPath, partPath); err != nil {
		return nil, fmt.Errorf("落位分片失败: %w", err)
	}

	s.mu.Lock()
	// 会话可能在 I/O 期间被 abort/TTL 清理；若已消失则清理刚落的片并报不存在。
	sess, ok = s.sessions[uploadID]
	if !ok {
		s.mu.Unlock()
		_ = os.Remove(partPath)
		return nil, ErrUploadNotFound
	}
	sess.received[index] = struct{}{}
	sess.lastActivity = s.nowTime()
	received := int64(len(sess.received))
	total := sess.chunkCount
	s.mu.Unlock()

	return &WriteChunkResult{Received: received, Total: total}, nil
}

// CompleteParams complete 参数。
type CompleteParams struct {
	// Codec 制品压缩算法（"zstd"|"none"），透传 PublishFile；本期发布恒 none。
	Codec string
	// ExpectedSHA256 期望的制品自身 sha256；非空则由 Ingest 比对，不符拒收。
	ExpectedSHA256 string
}

// Complete 完成分块上传：校验分片齐全 + 总字节匹配 → io.MultiReader 顺序拼装各 part 喂
// PublishFile 落 CAS → 返回 ClientFileResult（与单次上传逐字段一致）→ 清理临时分片 + 移除会话。
// 空文件会话（chunkCount=0）齐全校验天然通过、拼装为空流，正常走 PublishFile/Ingest 得空内容 CAS。
//
// 缺片返回 ErrUploadIncomplete；总字节不符返回 ErrInvalidChunkSize；频道不匹配 ErrUploadChannelMismatch。
// 落 CAS 失败时保留会话与临时分片（供重试 complete），不吞会话。
func (s *ChunkedUploadService) Complete(channelID, uploadID string, p CompleteParams) (*ClientFileResult, error) {
	s.mu.Lock()
	sess, ok := s.sessions[uploadID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrUploadNotFound
	}
	if sess.channelID != channelID {
		return nil, ErrUploadChannelMismatch
	}
	if int64(len(sess.received)) != sess.chunkCount {
		return nil, fmt.Errorf("%w: 已收 %d / %d", ErrUploadIncomplete, len(sess.received), sess.chunkCount)
	}

	// 顺序打开各 part 组 io.MultiReader（流式喂 Ingest，不额外落整文件）。
	files := make([]*os.File, 0, sess.chunkCount)
	readers := make([]io.Reader, 0, sess.chunkCount)
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	var openBytes int64
	for i := int64(0); i < sess.chunkCount; i++ {
		partPath := filepath.Join(sess.tempDir, fmt.Sprintf("%d.part", i))
		f, err := os.Open(partPath)
		if err != nil {
			closeAll()
			// 记录集与物理片不一致（异常删档）：等价缺片。
			return nil, fmt.Errorf("%w: 分片 %d 缺失文件", ErrUploadIncomplete, i)
		}
		if st, serr := f.Stat(); serr == nil {
			openBytes += st.Size()
		}
		files = append(files, f)
		readers = append(readers, f)
	}
	// 拼装后总字节须与声明一致（防末片声明/实际错位产坏制品）。
	if openBytes != sess.totalSize {
		closeAll()
		return nil, fmt.Errorf("%w: 拼装 %d 字节，声明 %d", ErrInvalidChunkSize, openBytes, sess.totalSize)
	}

	res, err := s.versions.PublishFile(io.MultiReader(readers...), PublishFileParams{
		Filename:       sess.filename,
		Codec:          p.Codec,
		ExpectedSHA256: p.ExpectedSHA256,
	})
	closeAll()
	if err != nil {
		// 落 CAS 失败（如 sha256 不符）：保留会话与临时分片，允许重试 complete。
		return nil, err
	}

	// 成功：移除会话 + 清临时分片。
	s.mu.Lock()
	delete(s.sessions, uploadID)
	s.mu.Unlock()
	_ = os.RemoveAll(sess.tempDir)
	return res, nil
}

// Abort 弃单：移除会话 + 清临时分片。会话不存在返回 ErrUploadNotFound（幂等语义由 handler 决定）。
// 频道不匹配返回 ErrUploadChannelMismatch（防跨频道弃他人会话）。
func (s *ChunkedUploadService) Abort(channelID, uploadID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[uploadID]
	if !ok {
		s.mu.Unlock()
		return ErrUploadNotFound
	}
	if sess.channelID != channelID {
		s.mu.Unlock()
		return ErrUploadChannelMismatch
	}
	delete(s.sessions, uploadID)
	s.mu.Unlock()
	_ = os.RemoveAll(sess.tempDir)
	return nil
}

// expectedChunkSize 计算第 index 片应有字节数：非末片为 chunkSize；末片为末段余量。
func (s *ChunkedUploadService) expectedChunkSize(sess *uploadSession, index int64) int64 {
	if index < sess.chunkCount-1 {
		return sess.chunkSize
	}
	// 末片 = 总字节 - 前 (chunkCount-1) 片；chunkCount>=1 时结果 ∈ [1, chunkSize]。
	last := sess.totalSize - sess.chunkSize*(sess.chunkCount-1)
	return last
}

// clampChunkSize 敲定分片大小：<=0 用默认，否则夹取到 [minChunkSize, maxChunkSize]。
func clampChunkSize(chunkSize int64) int64 {
	if chunkSize <= 0 {
		return defaultChunkSize
	}
	if chunkSize < minChunkSize {
		return minChunkSize
	}
	if chunkSize > maxChunkSize {
		return maxChunkSize
	}
	return chunkSize
}

// newUploadID 生成 16 字节随机十六进制上传会话标识（crypto/rand，不可预测防越权访问他人会话）。
func newUploadID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// receivedIndexes 返回会话已收分片序号的升序切片（仅测试/诊断辅助，不参与协议）。
func (sess *uploadSession) receivedIndexes() []int64 {
	out := make([]int64, 0, len(sess.received))
	for idx := range sess.received {
		out = append(out, idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
