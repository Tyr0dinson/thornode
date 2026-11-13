package thorchain

import (
	"errors"
	"slices"

	"gitlab.com/thorchain/thornode/v3/common"
)

func validateInstantSendGenesis(data GenesisState) error {
	if len(data.InstantSendStatus) > 1 {
		return errors.New("duplicate InstantSend status")
	}
	disabled := false
	for _, status := range data.InstantSendStatus {
		if !status.Chain.Equals(common.DASHChain) || status.MinHeight < 0 {
			return errors.New("invalid InstantSend status chain")
		}
		disabled = status.Disabled
		previous := ""
		for _, id := range status.Overdue {
			if !common.ValidDashHash(id.String()) || id.String() <= previous {
				return errors.New("invalid overdue InstantSend entries")
			}
			previous = id.String()
		}
	}
	credits := make(map[common.TxID]common.InstantSendCredit)
	inputs := make(map[string]common.TxID)
	for _, credit := range data.InstantSendCredits {
		_, found := credits[credit.Id]
		if found || !common.ValidDashHash(credit.Id.String()) || credit.Height < 0 || (!credit.Early && credit.Height == 0) {
			return errors.New("invalid InstantSend input credit")
		}
		credits[credit.Id] = credit
		err := common.ValidateDashInputs(credit.Inputs)
		if err != nil {
			return err
		}
		for _, input := range credit.Inputs {
			if _, exists := inputs[input.Outpoint()]; exists {
				return errors.New("duplicate DASH input credit")
			}
			inputs[input.Outpoint()] = credit.Id
		}
	}
	voters := make(map[common.TxID]ObservedTxVoter)
	for _, voter := range data.ObservedTxInVoters {
		voters[voter.TxID] = voter
	}
	vaults := make(map[common.PubKey]Vault)
	for _, vault := range data.Vaults {
		vaults[vault.PubKey] = vault
	}
	states := make(map[common.PubKey]bool)
	found := make(map[common.TxID]bool)
	for _, state := range data.InstantSend {
		err := state.Validate()
		if err != nil {
			return err
		}
		if states[state.PubKey] {
			return errors.New("duplicate InstantSend genesis vault")
		}
		states[state.PubKey] = true
		vault, exists := vaults[state.PubKey]
		if !exists || !vault.IsAsgard() || !vault.HasChain(state.Chain) {
			return errors.New("invalid InstantSend genesis vault")
		}
		for _, pending := range state.Pending {
			if found[pending.Id] {
				return errors.New("duplicate pending InstantSend deposit")
			}
			found[pending.Id] = true
			credit, exists := credits[pending.Id]
			if !exists || !credit.Early || credit.Height != 0 {
				return errors.New("pending InstantSend credit has no inputs")
			}
			voter := voters[pending.Id]
			err = validateInstantSendVoter(state, pending, voter)
			if err != nil {
				return err
			}
			if !slices.Equal(credit.Inputs, voter.Tx.SpentOutputRefs) {
				return errors.New("InstantSend voter inputs mismatch")
			}
		}
	}
	for _, status := range data.InstantSendStatus {
		for _, id := range status.Overdue {
			if !found[id] {
				return errors.New("overdue InstantSend deposit is not pending")
			}
		}
	}
	for _, credit := range data.InstantSendCredits {
		// A manual loss repair can retain input ownership after removing exposure,
		// but only with the permanent admission-disable flag set.
		if credit.Early && credit.Height == 0 && !found[credit.Id] && !disabled {
			return errors.New("unsettled InstantSend credit is not pending")
		}
	}
	return nil
}
