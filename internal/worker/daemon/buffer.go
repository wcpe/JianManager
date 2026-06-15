package daemon

import (
	"sync"
)

// RingBuffer 环形缓冲区，用于存储最近的输出。
// 支持多个观察者同时读取。
type RingBuffer struct {
	mu      sync.RWMutex
	buf     []byte
	size    int
	writePos int
	wrapped bool
}

// NewRingBuffer 创建指定大小的环形缓冲区。
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write 写入数据到环形缓冲区。
func (r *RingBuffer) Write(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, b := range data {
		r.buf[r.writePos] = b
		r.writePos++
		if r.writePos >= r.size {
			r.writePos = 0
			r.wrapped = true
		}
	}
}

// ReadAll 读取缓冲区中的所有数据（按写入顺序）。
func (r *RingBuffer) ReadAll() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.wrapped {
		result := make([]byte, r.writePos)
		copy(result, r.buf[:r.writePos])
		return result
	}

	// 已环绕：从 writePos 到末尾 + 从开头到 writePos
	result := make([]byte, r.size)
	copy(result, r.buf[r.writePos:])
	copy(result[r.size-r.writePos:], r.buf[:r.writePos])
	return result
}

// Len 返回缓冲区中的数据长度。
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.wrapped {
		return r.size
	}
	return r.writePos
}

// Reset 清空缓冲区。
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.writePos = 0
	r.wrapped = false
}
