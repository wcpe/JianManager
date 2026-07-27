package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateJWTSecret(t *testing.T) {
	assert.Error(t, ValidateJWTSecret("", false))
	assert.Error(t, ValidateJWTSecret(defaultJWTSecret, false))
	assert.NoError(t, ValidateJWTSecret("production-secret", false))
	assert.NoError(t, ValidateJWTSecret(defaultJWTSecret, true))
}
