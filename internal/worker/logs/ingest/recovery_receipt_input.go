package ingest

import (
	"crypto/sha256"
	"encoding/json"
)

// 同长度 WAL 的 Durable 或内容变化也必须使轮内凭据失效；不把正文副本常驻到下一轮。
func recoveryInputDigest(saved persistedSource) ([sha256.Size]byte, bool) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, entry := range saved.WAL {
		if err := encoder.Encode(entry); err != nil {
			return [sha256.Size]byte{}, false
		}
	}
	for _, event := range saved.Events {
		if err := encoder.Encode(event); err != nil {
			return [sha256.Size]byte{}, false
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, true
}
