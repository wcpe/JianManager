package main

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
)

func TestAssembleBotLoadServices_CreatesSharedProcessServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	bundle, err := assembleBotLoadServices(db, cpgrpc.NewClientPool(), "stable-server-secret")
	require.NoError(t, err)
	require.NotNil(t, bundle.capacity)
	require.NotNil(t, bundle.reservations)
	require.NotNil(t, bundle.signer)
	require.NotNil(t, bundle.preflight)
	require.NotNil(t, bundle.execution)
}

func TestAssembleBotLoadServices_RejectsMissingStableSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	_, err = assembleBotLoadServices(db, cpgrpc.NewClientPool(), "")
	require.Error(t, err)
}
