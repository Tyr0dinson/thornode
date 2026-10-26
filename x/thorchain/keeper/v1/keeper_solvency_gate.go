package keeperv1

import (
	"encoding/binary"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// solvencyGateHeightWidth zero-pads external heights so the store's byte order
// is ascending height order within one vault and chain.
const solvencyGateHeightWidth = 20

func (k KVStore) solvencyGatedInboundPrefix(chain common.Chain) []byte {
	return k.GetKey(prefixSolvencyGatedInbound, chain.String()+"/")
}

func (k KVStore) solvencyGatedInboundKey(in SolvencyGatedInbound) []byte {
	return k.GetKey(prefixSolvencyGatedInbound, fmt.Sprintf("%s/%0*d/%s", in.Chain, solvencyGateHeightWidth, in.Height, in.TxID))
}

// SetSolvencyGatedInbound parks a finalised inbound until its vault is attested
// solvent at or above the inbound's external height by a solvency consensus reached
// after in.ParkedAt.
func (k KVStore) SetSolvencyGatedInbound(ctx cosmos.Context, in SolvencyGatedInbound) error {
	if in.Height < 0 {
		return fmt.Errorf("invalid external height %d for solvency gated inbound %s", in.Height, in.TxID)
	}
	k.kvStore(ctx).Set(k.solvencyGatedInboundKey(in), k.cdc.MustMarshal(&in))
	return nil
}

// DeleteSolvencyGatedInbound removes a parked inbound.
func (k KVStore) DeleteSolvencyGatedInbound(ctx cosmos.Context, in SolvencyGatedInbound) {
	k.del(ctx, k.solvencyGatedInboundKey(in))
}

// GetSolvencyGatedInbounds returns up to limit parked inbounds on the chain, in
// ascending external height order.
func (k KVStore) GetSolvencyGatedInbounds(ctx cosmos.Context, chain common.Chain, limit int) ([]SolvencyGatedInbound, error) {
	iter := cosmos.KVStorePrefixIterator(k.kvStore(ctx), k.solvencyGatedInboundPrefix(chain))
	defer iter.Close()

	var inbounds []SolvencyGatedInbound
	for ; iter.Valid() && len(inbounds) < limit; iter.Next() {
		var in SolvencyGatedInbound
		if err := k.cdc.Unmarshal(iter.Value(), &in); err != nil {
			return inbounds, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", in, iter.Key()), err)
		}
		inbounds = append(inbounds, in)
	}
	return inbounds, nil
}

// GetSolvencyGatedInboundIterator iterates over the parked inbounds of every chain.
func (k KVStore) GetSolvencyGatedInboundIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixSolvencyGatedInbound)
}

func (k KVStore) solvencyConfirmationKey(chain common.Chain, pubKey common.PubKey) []byte {
	return k.GetKey(prefixSolvencyConfirmation, chain.String()+"/"+pubKey.String())
}

// SetSolvencyConfirmation records that the vault was attested solvent on the chain
// at the given external height, by a consensus reached in the current THORChain
// block. A confirmation for a lower external height than the stored one is ignored.
// The two int64 fields use big-endian encoding.
func (k KVStore) SetSolvencyConfirmation(ctx cosmos.Context, chain common.Chain, pubKey common.PubKey, externalHeight int64) error {
	current, _, err := k.GetSolvencyConfirmation(ctx, chain, pubKey)
	if err != nil {
		return err
	}
	if externalHeight < current {
		return nil
	}
	bz := make([]byte, 16)
	binary.BigEndian.PutUint64(bz[:8], uint64(externalHeight))
	binary.BigEndian.PutUint64(bz[8:], uint64(ctx.BlockHeight()))
	k.kvStore(ctx).Set(k.solvencyConfirmationKey(chain, pubKey), bz)
	return nil
}

// GetSolvencyConfirmation returns the highest external height at which the vault
// was attested solvent on the chain and the THORChain height of that consensus,
// or zeros if it never was.
func (k KVStore) GetSolvencyConfirmation(ctx cosmos.Context, chain common.Chain, pubKey common.PubKey) (externalHeight, thorHeight int64, err error) {
	bz := k.kvStore(ctx).Get(k.solvencyConfirmationKey(chain, pubKey))
	if bz == nil {
		return 0, 0, nil
	}
	if len(bz) != 16 {
		return 0, 0, fmt.Errorf("invalid solvency confirmation for %s on %s: expected 16 bytes, got %d", pubKey, chain, len(bz))
	}
	return int64(binary.BigEndian.Uint64(bz[:8])), int64(binary.BigEndian.Uint64(bz[8:])), nil
}
