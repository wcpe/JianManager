package grpc

import (
	"github.com/google/uuid"
)

func parseUUID(value string) (uuid.UUID, error) {
	return uuid.Parse(value)
}

// uuidV5 派生稳定 v5 UUID。
func uuidV5(namespace uuid.UUID, name string) (string, error) {
	return uuid.NewSHA1(namespace, []byte(name)).String(), nil
}