package daemon

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrame_EncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		channel Channel
		typ     Type
		flags   Flags
		payload []byte
	}{
		{"stdout data", ChannelStdout, TypeData, 0, []byte("hello world")},
		{"stderr data", ChannelStderr, TypeData, 0, []byte("error message")},
		{"empty payload", ChannelStdin, TypeCommand, 0, nil},
		{"control heartbeat", ChannelControl, TypeHeartbeat, 0, []byte("ping")},
		{"compressed flag", ChannelStdout, TypeData, FlagCompressed, []byte("compressed")},
		{"large payload", ChannelStdout, TypeData, 0, bytes.Repeat([]byte("x"), 1024)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := &Frame{
				Header: Header{
					Channel: tt.channel,
					Type:    tt.typ,
					Flags:   tt.flags,
				},
				Payload: tt.payload,
			}

			var buf bytes.Buffer
			err := frame.Encode(&buf)
			require.NoError(t, err)

			decoded, err := Decode(&buf)
			require.NoError(t, err)

			assert.Equal(t, tt.channel, decoded.Channel)
			assert.Equal(t, tt.typ, decoded.Type)
			assert.Equal(t, tt.flags, decoded.Flags)
			assert.Equal(t, tt.payload, decoded.Payload)
		})
	}
}

func TestFrame_HeaderSize(t *testing.T) {
	assert.Equal(t, 8, HeaderSize, "帧头应为 8 字节")
}

func TestDecode_EmptyPayload(t *testing.T) {
	frame := &Frame{
		Header:  Header{Channel: ChannelStdout, Type: TypeData},
		Payload: nil,
	}

	var buf bytes.Buffer
	err := frame.Encode(&buf)
	require.NoError(t, err)

	decoded, err := Decode(&buf)
	require.NoError(t, err)
	assert.Nil(t, decoded.Payload)
	assert.Equal(t, uint32(0), decoded.Length)
}

func TestDecode_InvalidPayloadSize(t *testing.T) {
	// 手动构造一个载荷长度超限的帧头
	var buf bytes.Buffer
	// Channel
	buf.Write([]byte{0, 1})
	// Type
	buf.WriteByte(0x01)
	// Flags
	buf.WriteByte(0)
	// Length (超过上限)
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})

	_, err := Decode(&buf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "超过上限")
}
