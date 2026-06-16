package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRingBuffer_WriteReadAll(t *testing.T) {
	buf := NewRingBuffer(16)

	buf.Write([]byte("hello"))
	assert.Equal(t, []byte("hello"), buf.ReadAll())
	assert.Equal(t, 5, buf.Len())
}

func TestRingBuffer_Overwrite(t *testing.T) {
	buf := NewRingBuffer(8)

	buf.Write([]byte("12345678"))
	assert.Equal(t, []byte("12345678"), buf.ReadAll())
	assert.Equal(t, 8, buf.Len())

	// 写入更多数据，覆盖旧数据
	buf.Write([]byte("AB"))
	assert.Equal(t, 8, buf.Len())
	result := buf.ReadAll()
	// 应该是 "345678AB"（前两个字节被覆盖）
	assert.Equal(t, []byte("345678AB"), result)
}

func TestRingBuffer_Reset(t *testing.T) {
	buf := NewRingBuffer(16)

	buf.Write([]byte("hello"))
	assert.Equal(t, 5, buf.Len())

	buf.Reset()
	assert.Equal(t, 0, buf.Len())
	assert.Equal(t, []byte{}, buf.ReadAll())
}

func TestRingBuffer_EmptyRead(t *testing.T) {
	buf := NewRingBuffer(16)
	assert.Equal(t, []byte{}, buf.ReadAll())
	assert.Equal(t, 0, buf.Len())
}
