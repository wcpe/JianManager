package daemon

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Channel 帧通道。
type Channel uint16

const (
	ChannelStdin   Channel = 0
	ChannelStdout  Channel = 1
	ChannelStderr  Channel = 2
	ChannelControl Channel = 3
)

// Type 帧类型。
type Type uint8

const (
	TypeData     Type = 0x01
	TypeCommand  Type = 0x02
	TypeResponse Type = 0x03
	TypeHeartbeat Type = 0x04
)

// Flags 帧标志位。
type Flags uint8

const (
	FlagCompressed Flags = 0x01 // bit0: zlib 压缩
)

// Header 帧头 (8 字节)。
type Header struct {
	Channel Channel // 2 bytes
	Type    Type    // 1 byte
	Flags   Flags   // 1 byte
	Length  uint32  // 4 bytes
}

const HeaderSize = 8
const MaxPayloadSize = 4 * 1024 * 1024 // 4MB

// Frame 完整帧。
type Frame struct {
	Header
	Payload []byte
}

// Encode 将帧编码写入 writer。
func (f *Frame) Encode(w io.Writer) error {
	// 写帧头
	if err := binary.Write(w, binary.BigEndian, f.Channel); err != nil {
		return fmt.Errorf("写 Channel 失败: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, f.Type); err != nil {
		return fmt.Errorf("写 Type 失败: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, f.Flags); err != nil {
		return fmt.Errorf("写 Flags 失败: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(f.Payload))); err != nil {
		return fmt.Errorf("写 Length 失败: %w", err)
	}

	// 写载荷
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return fmt.Errorf("写 Payload 失败: %w", err)
		}
	}

	return nil
}

// Decode 从 reader 解码读取帧。
func Decode(r io.Reader) (*Frame, error) {
	f := &Frame{}

	// 读帧头
	if err := binary.Read(r, binary.BigEndian, &f.Channel); err != nil {
		return nil, fmt.Errorf("读 Channel 失败: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &f.Type); err != nil {
		return nil, fmt.Errorf("读 Type 失败: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &f.Flags); err != nil {
		return nil, fmt.Errorf("读 Flags 失败: %w", err)
	}
	if err := binary.Read(r, binary.BigEndian, &f.Length); err != nil {
		return nil, fmt.Errorf("读 Length 失败: %w", err)
	}

	// 校验长度
	if f.Length > MaxPayloadSize {
		return nil, fmt.Errorf("载荷大小 %d 超过上限 %d", f.Length, MaxPayloadSize)
	}

	// 读载荷
	if f.Length > 0 {
		f.Payload = make([]byte, f.Length)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return nil, fmt.Errorf("读 Payload 失败: %w", err)
		}
	}

	return f, nil
}
