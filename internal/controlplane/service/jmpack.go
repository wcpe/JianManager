package service

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// 自有 .jmpack 分发容器格式（FR-097，见 ADR-021/022）。
//
// 布局（多字节整数大端）：
//
//	[0:6]   magic "JMPACK"
//	[6]     formatVersion uint8 = 1
//	[7]     flags uint8（bit0=encrypted 预留, bit1=diff 预留；首期 0）
//	[8:12]  metaLen uint32
//	[12:12+metaLen] meta JSON：{"files":[{path,sha256,size,codec,offset,clen}]}
//	[payload] 各文件压缩段顺序拼接（复用已存制品字节，codec 即制品 codec）
//	[尾部] keyIdLen uint8 + keyId + sig(Ed25519, 64B)
//
// **签名覆盖 bytes[0:payloadEnd] 原始字节**（非 canonical JSON）：两端对同一段原始字节签/验，跨语言天然一致。
const (
	jmpackMagic         = "JMPACK"
	jmpackFormatVersion = 1
	jmpackSigLen        = 64
)

var (
	// ErrJmPackFormat .jmpack 格式非法。
	ErrJmPackFormat = errors.New(".jmpack 格式非法")
	// ErrJmPackSignature .jmpack 验签失败（签名缺失/不符/未知 keyId）。
	ErrJmPackSignature = errors.New(".jmpack 验签失败")
)

// JmPackInput 一个文件：解压后元数据 + 已存制品（压缩态）字节。
type JmPackInput struct {
	// Path 相对 gameDir 的 POSIX 路径。
	Path string
	// SHA256 解压后原始内容 sha256（信任校验）。
	SHA256 string
	// Size 解压后原始大小。
	Size int64
	// Codec zstd | none（即制品 codec）。
	Codec string
	// Data 制品（压缩态）字节。
	Data []byte
}

type jmpackMetaFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Codec  string `json:"codec"`
	Offset int64  `json:"offset"`
	CLen   int64  `json:"clen"`
}

type jmpackMeta struct {
	Files []jmpackMetaFile `json:"files"`
}

// PackJmPack 打包 + 签名。sign 对 magic→payload 末的原始字节做 Ed25519 签名，返回 (sig, keyId)。
func PackJmPack(files []JmPackInput, sign func(msg []byte) (sig []byte, keyID string)) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: 空文件集", ErrJmPackFormat)
	}
	meta := jmpackMeta{Files: make([]jmpackMetaFile, 0, len(files))}
	var payload bytes.Buffer
	var offset int64
	for _, f := range files {
		clen := int64(len(f.Data))
		meta.Files = append(meta.Files, jmpackMetaFile{
			Path: f.Path, SHA256: f.SHA256, Size: f.Size, Codec: f.Codec, Offset: offset, CLen: clen,
		})
		payload.Write(f.Data)
		offset += clen
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("序列化 .jmpack meta 失败: %w", err)
	}

	var head bytes.Buffer
	head.WriteString(jmpackMagic)
	head.WriteByte(jmpackFormatVersion)
	head.WriteByte(0) // flags：首期 0。
	_ = binary.Write(&head, binary.BigEndian, uint32(len(metaJSON)))
	head.Write(metaJSON)
	head.Write(payload.Bytes())
	body := head.Bytes() // 签名覆盖范围。

	sig, keyID := sign(body)
	if len(sig) != jmpackSigLen {
		return nil, fmt.Errorf("%w: 签名长度异常", ErrJmPackSignature)
	}
	out := make([]byte, 0, len(body)+1+len(keyID)+jmpackSigLen)
	out = append(out, body...)
	out = append(out, byte(len(keyID)))
	out = append(out, []byte(keyID)...)
	out = append(out, sig...)
	return out, nil
}

// ParseJmPack 验签 + 解析（**不解压**，返回每文件压缩态 Data + codec，由调用方按 codec 解压）。
// pubFor 据 keyId 返回内置公钥（nil=未知 keyId → 拒）。验签失败/格式非法即返回错误。
func ParseJmPack(data []byte, pubFor func(keyID string) ed25519.PublicKey) ([]JmPackInput, error) {
	if len(data) < 12 || string(data[0:6]) != jmpackMagic {
		return nil, ErrJmPackFormat
	}
	if data[6] != jmpackFormatVersion {
		return nil, fmt.Errorf("%w: 不支持的格式版本 %d", ErrJmPackFormat, data[6])
	}
	metaLen := int(binary.BigEndian.Uint32(data[8:12]))
	metaEnd := 12 + metaLen
	if metaLen < 0 || metaEnd > len(data) {
		return nil, ErrJmPackFormat
	}
	var meta jmpackMeta
	if err := json.Unmarshal(data[12:metaEnd], &meta); err != nil {
		return nil, fmt.Errorf("%w: meta 解析失败", ErrJmPackFormat)
	}
	var payloadLen int64
	for _, f := range meta.Files {
		if f.Offset < 0 || f.CLen < 0 {
			return nil, ErrJmPackFormat
		}
		payloadLen += f.CLen
	}
	payloadStart := metaEnd
	payloadEnd := payloadStart + int(payloadLen)
	if payloadEnd > len(data) {
		return nil, ErrJmPackFormat
	}

	sigSection := data[payloadEnd:]
	if len(sigSection) < 1 {
		return nil, ErrJmPackFormat
	}
	keyIDLen := int(sigSection[0])
	if len(sigSection) < 1+keyIDLen+jmpackSigLen {
		return nil, ErrJmPackFormat
	}
	keyID := string(sigSection[1 : 1+keyIDLen])
	sig := sigSection[1+keyIDLen : 1+keyIDLen+jmpackSigLen]
	pub := pubFor(keyID)
	if pub == nil {
		return nil, fmt.Errorf("%w: 未知 keyId %q", ErrJmPackSignature, keyID)
	}
	if !ed25519.Verify(pub, data[0:payloadEnd], sig) {
		return nil, ErrJmPackSignature
	}

	out := make([]JmPackInput, 0, len(meta.Files))
	for _, f := range meta.Files {
		start := payloadStart + int(f.Offset)
		end := start + int(f.CLen)
		if end > payloadEnd {
			return nil, ErrJmPackFormat
		}
		out = append(out, JmPackInput{
			Path: f.Path, SHA256: f.SHA256, Size: f.Size, Codec: f.Codec, Data: data[start:end],
		})
	}
	return out, nil
}
