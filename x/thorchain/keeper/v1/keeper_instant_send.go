package keeperv1

import (
	"errors"
	"fmt"
	"slices"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	storetypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

const (
	prefixInstantSend       storetypes.DbPrefix = "instant_send/"
	prefixInstantSendStatus storetypes.DbPrefix = "instant_send_status/"
	prefixInstantSendCredit storetypes.DbPrefix = "instant_send_credit/" // #nosec G101 -- database prefix, not a credential.
	prefixInstantSendExpiry storetypes.DbPrefix = "instant_send_expiry/"
	prefixInstantSendInput  storetypes.DbPrefix = "instant_send_input/"
)

// GetInstantSend reads one vault's consensus-owned admission set.
// A missing record is an empty set with the requested identity.
func (k KVStore) GetInstantSend(ctx cosmos.Context, chain common.Chain, pubKey common.PubKey) (common.InstantSendState, error) {
	state := common.InstantSendState{Chain: chain, PubKey: pubKey}
	data := k.kvStore(ctx).Get(k.GetKey(prefixInstantSend, chain.String()+"/"+pubKey.String()))
	if data == nil {
		return state, nil
	}
	err := k.cdc.Unmarshal(data, &state)
	if err != nil {
		return state, err
	}
	if !state.Chain.Equals(chain) || !state.PubKey.Equals(pubKey) {
		return state, fmt.Errorf("InstantSend accounting chain or vault mismatch")
	}
	return state, state.Validate()
}

// ListInstantSend returns one chain's records in deterministic store order.
// EmptyChain is reserved for genesis export, which needs all chains.
func (k KVStore) ListInstantSend(ctx cosmos.Context, chain common.Chain) ([]common.InstantSendState, error) {
	prefix := prefixInstantSend
	if !chain.IsEmpty() {
		prefix = storetypes.DbPrefix(k.GetKey(prefix, chain.String()+"/"))
	}
	iter := k.getIterator(ctx, prefix)
	defer iter.Close()
	var states []common.InstantSendState
	for ; iter.Valid(); iter.Next() {
		var state common.InstantSendState
		err := k.cdc.Unmarshal(iter.Value(), &state)
		if err != nil {
			return nil, err
		}
		err = state.Validate()
		if err != nil {
			return nil, err
		}
		if !chain.IsEmpty() && !state.Chain.Equals(chain) {
			return nil, fmt.Errorf("InstantSend accounting chain mismatch")
		}
		states = append(states, state)
	}
	return states, nil
}

func (k KVStore) SetInstantSend(ctx cosmos.Context, state common.InstantSendState) error {
	err := state.Validate()
	if err != nil {
		return err
	}
	key := k.GetKey(prefixInstantSend, state.Chain.String()+"/"+state.PubKey.String())
	if len(state.Pending) == 0 {
		k.kvStore(ctx).Delete(key)
		return nil
	}
	data, err := k.cdc.Marshal(&state)
	if err != nil {
		return err
	}
	k.kvStore(ctx).Set(key, data)
	return nil
}

func (k KVStore) GetInstantSendStatus(ctx cosmos.Context, key common.Chain) (common.InstantSendStatus, error) {
	value := common.InstantSendStatus{Chain: key}
	data := k.kvStore(ctx).Get(k.GetKey(prefixInstantSendStatus, key.String()))
	if data == nil {
		return value, nil
	}
	err := k.cdc.Unmarshal(data, &value)
	return value, err
}

func (k KVStore) ListInstantSendStatus(ctx cosmos.Context) ([]common.InstantSendStatus, error) {
	iter := k.getIterator(ctx, prefixInstantSendStatus)
	defer iter.Close()
	var values []common.InstantSendStatus
	for ; iter.Valid(); iter.Next() {
		var value common.InstantSendStatus
		err := k.cdc.Unmarshal(iter.Value(), &value)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (k KVStore) SetInstantSendStatus(ctx cosmos.Context, value common.InstantSendStatus) error {
	if !value.Chain.Equals(common.DASHChain) || value.MinHeight < 0 {
		return errors.New("invalid InstantSend status")
	}
	previous, err := k.GetInstantSendStatus(ctx, value.Chain)
	if err != nil {
		return err
	}
	if value.MinHeight < previous.MinHeight {
		return errors.New("InstantSend minimum height cannot decrease")
	}
	if value.Disabled == previous.Disabled && value.MinHeight == previous.MinHeight && slices.Equal(value.Overdue, previous.Overdue) {
		return nil
	}
	data, marshalErr := k.cdc.Marshal(&value)
	if marshalErr != nil {
		return marshalErr
	}
	k.kvStore(ctx).Set(k.GetKey(prefixInstantSendStatus, value.Chain.String()), data)
	return nil
}

func instantSendExpiryKey(value common.InstantSendCredit) string {
	return fmt.Sprintf("%020d/%s", value.Height, value.Id)
}

func (k KVStore) GetInstantSendCredit(ctx cosmos.Context, key common.TxID) (common.InstantSendCredit, error) {
	value := common.InstantSendCredit{}
	data := k.kvStore(ctx).Get(k.GetKey(prefixInstantSendCredit, key.String()))
	if data == nil {
		return value, nil
	}
	err := k.cdc.Unmarshal(data, &value)
	return value, err
}

func (k KVStore) ListInstantSendCredits(ctx cosmos.Context) ([]common.InstantSendCredit, error) {
	iter := k.getIterator(ctx, prefixInstantSendCredit)
	defer iter.Close()
	var values []common.InstantSendCredit
	for ; iter.Valid(); iter.Next() {
		var value common.InstantSendCredit
		err := k.cdc.Unmarshal(iter.Value(), &value)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (k KVStore) SetInstantSendCredit(ctx cosmos.Context, value common.InstantSendCredit) error {
	if !common.ValidDashHash(value.Id.String()) || value.Height < 0 || (!value.Early && value.Height == 0) {
		return errors.New("invalid input credit")
	}
	err := common.ValidateDashInputs(value.Inputs)
	if err != nil {
		return err
	}
	previous, err := k.GetInstantSendCredit(ctx, value.Id)
	if err != nil {
		return err
	}
	if !previous.Id.IsEmpty() {
		if previous.Early != value.Early || !slices.Equal(previous.Inputs, value.Inputs) || value.Height < previous.Height {
			return errors.New("conflicting input credit")
		}
	}
	for _, input := range value.Inputs {
		owner, readErr := k.GetInstantSendInput(ctx, input)
		if readErr != nil {
			return readErr
		}
		if !owner.Id.IsEmpty() && !owner.Id.Equals(value.Id) {
			return errors.New("DASH input already credited")
		}
	}
	data, marshalErr := k.cdc.Marshal(&value)
	if marshalErr != nil {
		return marshalErr
	}
	for _, input := range value.Inputs {
		k.kvStore(ctx).Set(k.GetKey(prefixInstantSendInput, input.Outpoint()), []byte(value.Id))
	}
	if previous.Height > 0 {
		k.kvStore(ctx).Delete(k.GetKey(prefixInstantSendExpiry, instantSendExpiryKey(previous)))
	}
	k.kvStore(ctx).Set(k.GetKey(prefixInstantSendCredit, value.Id.String()), data)
	if value.Height > 0 {
		k.kvStore(ctx).Set(k.GetKey(prefixInstantSendExpiry, instantSendExpiryKey(value)), []byte(value.Id))
	}
	return nil
}

// The caller commits this batch together with its minimum accepted anchor.
// Never split one credit's input mappings across blocks.
func (k KVStore) PruneInstantSendCredits(ctx cosmos.Context, height int64, limit int) error {
	iter := k.getIterator(ctx, prefixInstantSendExpiry)
	var expired []common.InstantSendCredit
	for ; iter.Valid(); iter.Next() {
		credit, err := k.GetInstantSendCredit(ctx, common.TxID(iter.Value()))
		if err != nil || credit.Id.IsEmpty() || credit.Height <= 0 {
			iter.Close()
			return errors.New("invalid InstantSend expiry record")
		}
		if credit.Height >= height || len(credit.Inputs)+2 > limit {
			break
		}
		expired = append(expired, credit)
		limit -= len(credit.Inputs) + 2
	}
	iter.Close()
	for _, credit := range expired {
		for _, input := range credit.Inputs {
			k.kvStore(ctx).Delete(k.GetKey(prefixInstantSendInput, input.Outpoint()))
		}
		k.kvStore(ctx).Delete(k.GetKey(prefixInstantSendCredit, credit.Id.String()))
		k.kvStore(ctx).Delete(k.GetKey(prefixInstantSendExpiry, instantSendExpiryKey(credit)))
	}
	return nil
}

func (k KVStore) GetInstantSendInput(ctx cosmos.Context, input common.OutputRef) (common.InstantSendCredit, error) {
	data := k.kvStore(ctx).Get(k.GetKey(prefixInstantSendInput, input.Outpoint()))
	if data == nil {
		return common.InstantSendCredit{}, nil
	}
	return k.GetInstantSendCredit(ctx, common.TxID(data))
}
