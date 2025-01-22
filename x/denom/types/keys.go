package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "denom"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// RouterKey is the message route for slashing
	RouterKey = ModuleName

	// QuerierRoute defines the module's query routing key
	QuerierRoute = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_denom"
)

var (
	DenomAdminKey    = "admins"
	DenomAdminPrefix = collections.NewPrefix(0)
)
