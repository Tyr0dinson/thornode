package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Valid check whether the node status is valid or not
func (x NodeStatus) Valid() error {
	if _, ok := NodeStatus_value[strings.Title(x.String())]; !ok { // nolint SA1019
		return fmt.Errorf("invalid node status")
	}
	return nil
}

// MarshalJSON marshal NodeStatus to JSON in string form
func (x NodeStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(x.String())
}

// UnmarshalJSON convert string form back to NodeStatus
func (x *NodeStatus) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*x = getNodeStatus(s)
	return nil
}

// getNodeStatus from string
func getNodeStatus(ps string) NodeStatus {
	if val, ok := NodeStatus_value[strings.Title(ps)]; ok { // nolint SA1019
		return NodeStatus(val)
	}
	return NodeStatus_Unknown
}

func NewBondProviders(acc cosmos.AccAddress) BondProviders {
	return BondProviders{
		NodeAddress:     acc,
		NodeOperatorFee: cosmos.ZeroUint(),
		Providers:       make([]BondProvider, 0),
	}
}

func NewBondProvider(acc cosmos.AccAddress) BondProvider {
	return BondProvider{
		BondAddress:    acc,
		Bond:           cosmos.ZeroUint(),
		Principal:      cosmos.ZeroUint(),
		ClaimableYield: cosmos.ZeroUint(),
		PayoutAsset:    common.EmptyAsset,
		PayoutAddress:  common.NoAddress,
	}
}

// NewNodeAccount create new instance of NodeAccount
func NewNodeAccount(nodeAddress cosmos.AccAddress, status NodeStatus, nodePubKeySet common.PubKeySet, validatorConsPubKey string, bond cosmos.Uint, bondAddress common.Address, height int64) NodeAccount {
	na := NodeAccount{
		NodeAddress:         nodeAddress,
		PubKeySet:           nodePubKeySet,
		ValidatorConsPubKey: validatorConsPubKey,
		Bond:                bond,
		BondAddress:         bondAddress,
	}
	na.UpdateStatus(status, height)
	return na
}

// IsEmpty decide whether NodeAccount is empty
func (m *NodeAccount) IsEmpty() bool {
	return m.NodeAddress.Empty() || m.Status == NodeStatus_Unknown
}

// Valid check whether NodeAccount has all necessary values
func (m *NodeAccount) Valid() error {
	if m.NodeAddress.Empty() {
		return errors.New("node thor address is empty")
	}
	if m.BondAddress.IsEmpty() {
		return errors.New("bond address is empty")
	}
	if m.Status == NodeStatus_Unknown {
		return errors.New("node status cannot be unknown")
	}

	return nil
}

// GetSignerMembership return a list of pubkey that the node are part of
func (m *NodeAccount) GetSignerMembership() common.PubKeys {
	pubkeys := make(common.PubKeys, 0)
	for _, pk := range m.SignerMembership {
		pk, err := common.NewPubKey(pk)
		if err != nil {
			continue
		}
		pubkeys = append(pubkeys, pk)
	}
	return pubkeys
}

// GetVersion return the node account's version
func (m *NodeAccount) GetVersion() semver.Version {
	version, _ := semver.Make(m.Version)
	return version
}

// UpdateStatus change the status of node account, in the mean time update StatusSince field
func (m *NodeAccount) UpdateStatus(status NodeStatus, height int64) {
	if m.Status == status {
		return
	}
	m.Status = status
	m.StatusSince = height
	if status != NodeStatus_Active {
		m.ActiveBlockHeight = 0
	}
}

// Equals compare two node account, to see whether they are equal
func (m *NodeAccount) Equals(n1 NodeAccount) bool {
	if m.NodeAddress.Equals(n1.NodeAddress) &&
		m.PubKeySet.Equals(n1.PubKeySet) &&
		m.ValidatorConsPubKey == n1.ValidatorConsPubKey &&
		m.BondAddress.Equals(n1.BondAddress) &&
		m.Bond.Equal(n1.Bond) &&
		m.GetVersion().Equals(n1.GetVersion()) {
		return true
	}
	return false
}

// String implement fmt.Stringer interface
func (m *NodeAccount) String() string {
	sb := strings.Builder{}
	sb.WriteString("node:" + m.NodeAddress.String() + "\n")
	sb.WriteString("status:" + m.Status.String() + "\n")
	sb.WriteString("node pubkeys:" + m.PubKeySet.String() + "\n")
	sb.WriteString("validator consensus pub key:" + m.ValidatorConsPubKey + "\n")
	sb.WriteString("bond:" + m.Bond.String() + "\n")
	sb.WriteString("version:" + m.Version + "\n")
	sb.WriteString("bond address:" + m.BondAddress.String() + "\n")
	sb.WriteString("requested to leave:" + strconv.FormatBool(m.RequestedToLeave) + "\n")
	return sb.String()
}

// CalcBondUnits calculate bond
func (m *NodeAccount) CalcBondUnits(height, slashpoints int64) cosmos.Uint {
	// ensure slashpoints is not negative
	slashpoints = int64(math.Max(float64(0), float64(slashpoints)))
	if height < 0 || m.ActiveBlockHeight < 0 || slashpoints < 0 {
		return cosmos.ZeroUint()
	}

	blockCount := height - (m.ActiveBlockHeight + slashpoints)
	if blockCount < 0 { // ensure we're never negative
		blockCount = 0
	}

	return cosmos.NewUint(uint64(blockCount))
}

// TryAddSignerPubKey add a key to node account
func (m *NodeAccount) TryAddSignerPubKey(key common.PubKey) {
	if key.IsEmpty() {
		return
	}
	for _, item := range m.GetSignerMembership() {
		if item.Equals(key) {
			return
		}
	}
	m.SignerMembership = append(m.SignerMembership, key.String())
}

// TryRemoveSignerPubKey remove the given pubkey from signer membership
func (m *NodeAccount) TryRemoveSignerPubKey(key common.PubKey) {
	if key.IsEmpty() {
		return
	}
	idxToDelete := -1
	for idx, item := range m.GetSignerMembership() {
		if item.Equals(key) {
			idxToDelete = idx
		}
	}
	if idxToDelete != -1 {
		m.SignerMembership = append(m.SignerMembership[:idxToDelete], m.SignerMembership[idxToDelete+1:]...)
	}
}

// NodeAccounts just a list of NodeAccount
type NodeAccounts []NodeAccount

// IsEmpty to check whether the NodeAccounts is empty
func (nas NodeAccounts) IsEmpty() bool {
	return len(nas) == 0
}

// IsNodeKeys validate whether the given account address belongs to an currently active validator
func (nas NodeAccounts) IsNodeKeys(addr cosmos.AccAddress) bool {
	for _, na := range nas {
		if na.Status == NodeStatus_Active && addr.Equals(na.NodeAddress) {
			return true
		}
	}
	return false
}

// Less is used for sorting - first by status, then by node address.
func (nas NodeAccounts) Less(i, j int) bool {
	if nas[i].StatusSince < nas[j].StatusSince {
		return true
	}
	if nas[i].StatusSince > nas[j].StatusSince {
		return false
	}
	return nas[i].NodeAddress.String() < nas[j].NodeAddress.String()
}

// Len return the number of accounts in it
func (nas NodeAccounts) Len() int { return len(nas) }

// Swap node account
func (nas NodeAccounts) Swap(i, j int) {
	nas[i], nas[j] = nas[j], nas[i]
}

// Contains will check whether the given node account is in the list
func (nas NodeAccounts) Contains(na NodeAccount) bool {
	for _, item := range nas {
		if item.Equals(na) {
			return true
		}
	}
	return false
}

func (nas NodeAccounts) GetNodeAddresses() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, len(nas))
	for i, na := range nas {
		addrs[i] = na.NodeAddress
	}
	return addrs
}

func (m *BondProvider) IsEmpty() bool {
	return m.BondAddress.Empty()
}

// NormaliseYieldAccounting initializes legacy or nil bond yield fields before accounting.
func (m *BondProvider) NormaliseYieldAccounting() {
	if m.Bond.IsNil() {
		m.Bond = cosmos.ZeroUint()
	}
	if m.Principal.IsNil() {
		m.Principal = cosmos.ZeroUint()
	}
	if m.ClaimableYield.IsNil() {
		m.ClaimableYield = cosmos.ZeroUint()
	}
	if m.PayoutAddress.IsEmpty() {
		m.PayoutAddress = common.NoAddress
	}
	// Before this accounting existed, the whole provider bond was principal.
	if m.Principal.IsZero() && m.ClaimableYield.IsZero() && !m.Bond.IsZero() {
		m.Principal = m.Bond
	}
	m.ClampYieldAccounting()
}

// ClampYieldAccounting keeps principal and claimable yield within the provider bond.
func (m *BondProvider) ClampYieldAccounting() {
	if m.Bond.IsNil() {
		m.Bond = cosmos.ZeroUint()
	}
	if m.Principal.IsNil() {
		m.Principal = cosmos.ZeroUint()
	}
	if m.ClaimableYield.IsNil() {
		m.ClaimableYield = cosmos.ZeroUint()
	}
	if m.Principal.Add(m.ClaimableYield).LTE(m.Bond) {
		return
	}
	if m.Principal.GTE(m.Bond) {
		m.Principal = m.Bond
		m.ClaimableYield = cosmos.ZeroUint()
		return
	}
	m.ClaimableYield = common.SafeSub(m.Bond, m.Principal)
}

// NormaliseYieldAccounting initializes yield accounting on every bond provider.
func (bp *BondProviders) NormaliseYieldAccounting() {
	for i := range bp.Providers {
		bp.Providers[i].NormaliseYieldAccounting()
	}
}

// ResetYieldAccounting treats the current bond as principal and clears claimable yield.
func (bp *BondProviders) ResetYieldAccounting() {
	for i := range bp.Providers {
		if bp.Providers[i].Bond.IsNil() {
			bp.Providers[i].Bond = cosmos.ZeroUint()
		}
		bp.Providers[i].Principal = bp.Providers[i].Bond
		bp.Providers[i].ClaimableYield = cosmos.ZeroUint()
		if bp.Providers[i].PayoutAddress.IsEmpty() {
			bp.Providers[i].PayoutAddress = common.NoAddress
		}
	}
}

// BondSnapshot captures provider bond balances before reward distribution.
func (bp *BondProviders) BondSnapshot() map[string]cosmos.Uint {
	snapshot := make(map[string]cosmos.Uint, len(bp.Providers))
	for i := range bp.Providers {
		bp.Providers[i].NormaliseYieldAccounting()
		snapshot[bp.Providers[i].BondAddress.String()] = bp.Providers[i].Bond
	}
	return snapshot
}

// AccrueClaimableYield adds post-reward bond increases to claimable yield, bounded by the cap.
func (bp *BondProviders) AccrueClaimableYield(before map[string]cosmos.Uint, claimableBpsCap cosmos.Uint) {
	for i := range bp.Providers {
		provider := &bp.Providers[i]
		provider.NormaliseYieldAccounting()
		beforeBond, ok := before[provider.BondAddress.String()]
		if !ok || provider.Bond.LTE(beforeBond) {
			provider.ClampYieldAccounting()
			continue
		}

		delta := provider.Bond.Sub(beforeBond)
		if !claimableBpsCap.IsZero() {
			// The cap is based on principal, so repeatedly compounding rewards cannot
			// make all provider bond claimable.
			maxClaimable := common.GetSafeShare(claimableBpsCap, cosmos.NewUint(10000), provider.Principal)
			if provider.ClaimableYield.GTE(maxClaimable) {
				provider.ClampYieldAccounting()
				continue
			}
			headroom := maxClaimable.Sub(provider.ClaimableYield)
			if delta.GT(headroom) {
				delta = headroom
			}
		}
		provider.ClaimableYield = provider.ClaimableYield.Add(delta)
		provider.ClampYieldAccounting()
	}
}

func (bp *BondProviders) Has(acc cosmos.AccAddress) bool {
	provider := bp.Get(acc)
	return !provider.IsEmpty()
}

func (bp *BondProviders) Get(acc cosmos.AccAddress) BondProvider {
	for _, provider := range bp.Providers {
		if provider.BondAddress.Equals(acc) {
			return provider
		}
	}
	return BondProvider{}
}

func (bp *BondProviders) Bond(bond cosmos.Uint, acc cosmos.AccAddress) {
	for i, provider := range bp.Providers {
		if provider.BondAddress.Equals(acc) {
			bp.Providers[i].NormaliseYieldAccounting()
			bp.Providers[i].Bond = bp.Providers[i].Bond.Add(bond)
			// Fresh bond deposits are principal, not claimable yield.
			bp.Providers[i].Principal = bp.Providers[i].Principal.Add(bond)
			return
		}
	}
}

func (bp *BondProviders) Unbond(bond cosmos.Uint, acc cosmos.AccAddress) {
	for i, provider := range bp.Providers {
		if provider.BondAddress.Equals(acc) {
			bp.Providers[i].NormaliseYieldAccounting()
			bp.Providers[i].Bond = common.SafeSub(bp.Providers[i].Bond, bond)
			// Unbonding consumes claimable yield first so principal only shrinks after
			// accrued yield has been exhausted.
			claimableReduction := bond
			if claimableReduction.GT(bp.Providers[i].ClaimableYield) {
				claimableReduction = bp.Providers[i].ClaimableYield
			}
			bp.Providers[i].ClaimableYield = common.SafeSub(bp.Providers[i].ClaimableYield, claimableReduction)
			principalReduction := common.SafeSub(bond, claimableReduction)
			bp.Providers[i].Principal = common.SafeSub(bp.Providers[i].Principal, principalReduction)
			bp.Providers[i].ClampYieldAccounting()
			return
		}
	}
}

// ClaimYield removes claimable yield from a provider without reducing principal.
func (bp *BondProviders) ClaimYield(bond cosmos.Uint, acc cosmos.AccAddress) cosmos.Uint {
	for i, provider := range bp.Providers {
		if provider.BondAddress.Equals(acc) {
			bp.Providers[i].NormaliseYieldAccounting()
			if bond.IsZero() || bond.GT(bp.Providers[i].ClaimableYield) {
				bond = bp.Providers[i].ClaimableYield
			}
			bp.Providers[i].Bond = common.SafeSub(bp.Providers[i].Bond, bond)
			bp.Providers[i].ClaimableYield = common.SafeSub(bp.Providers[i].ClaimableYield, bond)
			bp.Providers[i].ClampYieldAccounting()
			return bond
		}
	}
	return cosmos.ZeroUint()
}

// SetAutoClaim stores the provider's automatic bond yield claim settings.
func (bp *BondProviders) SetAutoClaim(acc cosmos.AccAddress, bps uint64, payoutAsset common.Asset, payoutAddress common.Address) bool {
	for i, provider := range bp.Providers {
		if provider.BondAddress.Equals(acc) {
			bp.Providers[i].NormaliseYieldAccounting()
			bp.Providers[i].AutoClaimBps = bps
			bp.Providers[i].PayoutAsset = payoutAsset
			if payoutAddress.IsEmpty() {
				payoutAddress = common.NoAddress
			}
			bp.Providers[i].PayoutAddress = payoutAddress
			return true
		}
	}
	return false
}

func (bp *BondProviders) Rebond(bond cosmos.Uint, oldAcc, newAcc cosmos.AccAddress) cosmos.Uint {
	for oldIdx := range bp.Providers {
		if !bp.Providers[oldIdx].BondAddress.Equals(oldAcc) {
			continue
		}
		for newIdx := range bp.Providers {
			if !bp.Providers[newIdx].BondAddress.Equals(newAcc) {
				continue
			}
			bp.Providers[oldIdx].NormaliseYieldAccounting()
			bp.Providers[newIdx].NormaliseYieldAccounting()
			oldProvider := bp.Providers[oldIdx]
			if bond.IsZero() || bond.GT(oldProvider.Bond) {
				bond = oldProvider.Bond
			}
			if bond.IsZero() {
				return cosmos.ZeroUint()
			}

			// Rebonding moves principal and claimable yield in the same proportion as
			// the provider bond being transferred.
			principal := common.GetSafeShare(bond, oldProvider.Bond, oldProvider.Principal)
			claimable := common.GetSafeShare(bond, oldProvider.Bond, oldProvider.ClaimableYield)
			if bond.Equal(oldProvider.Bond) {
				principal = oldProvider.Principal
				claimable = oldProvider.ClaimableYield
			}

			bp.Providers[oldIdx].Bond = common.SafeSub(bp.Providers[oldIdx].Bond, bond)
			bp.Providers[oldIdx].Principal = common.SafeSub(bp.Providers[oldIdx].Principal, principal)
			bp.Providers[oldIdx].ClaimableYield = common.SafeSub(bp.Providers[oldIdx].ClaimableYield, claimable)
			bp.Providers[oldIdx].ClampYieldAccounting()

			bp.Providers[newIdx].Bond = bp.Providers[newIdx].Bond.Add(bond)
			bp.Providers[newIdx].Principal = bp.Providers[newIdx].Principal.Add(principal)
			bp.Providers[newIdx].ClaimableYield = bp.Providers[newIdx].ClaimableYield.Add(claimable)
			bp.Providers[newIdx].ClampYieldAccounting()

			return bond
		}
	}
	return cosmos.ZeroUint()
}

// remove provider (only if bond is zero)
func (bp *BondProviders) Remove(acc cosmos.AccAddress) bool {
	for i, provider := range bp.Providers {
		if i == 0 {
			// cannot remove the first bond provider
			continue
		}
		if provider.BondAddress.Equals(acc) && provider.Bond.IsZero() {
			bp.Providers = append(bp.Providers[:i], bp.Providers[i+1:]...)
			return true
		}
	}
	return false
}

// realigns the bond providers relative to the node bond
func (bp *BondProviders) Adjust(nodeBond cosmos.Uint) {
	bp.NormaliseYieldAccounting()
	totalBond := cosmos.ZeroUint()
	if len(bp.Providers) == 0 {
		// no adjustment needed
		return
	}

	for _, provider := range bp.Providers {
		totalBond = totalBond.Add(provider.Bond)
	}

	if totalBond.Equal(nodeBond) {
		// no adjustment needed
		return
	}

	// deduct node operator fee from income
	fee := cosmos.ZeroUint()
	if totalBond.LT(nodeBond) {
		surplus := common.SafeSub(nodeBond, totalBond)
		fee = common.GetSafeShare(bp.NodeOperatorFee, cosmos.NewUint(10000), surplus)
	}
	nodeBond = common.SafeSub(nodeBond, fee)

	// first bond provider is node operator
	// To have an invariant bond sum, subtract the non-operator bonds from the total.
	bp.Providers[0].Bond = nodeBond
	for i := range bp.Providers {
		if i == 0 {
			continue
		}
		bond := bp.Providers[i].Bond
		bp.Providers[i].Bond = common.GetSafeShare(bond, totalBond, nodeBond)
		bp.Providers[0].Bond = common.SafeSub(bp.Providers[0].Bond, bp.Providers[i].Bond)

	}
	bp.Providers[0].Bond = bp.Providers[0].Bond.Add(fee)
	for i := range bp.Providers {
		bp.Providers[i].ClampYieldAccounting()
	}
}

// HasProviderBonded: Checks if a bond provider (not the operator) has bonded to the node
func (bp *BondProviders) HasProviderBonded(opBondAddress cosmos.AccAddress) bool {
	for i := range bp.Providers {
		if !bp.Providers[i].BondAddress.Equals(opBondAddress) && bp.Providers[i].Bond.GT(cosmos.ZeroUint()) {
			return true
		}
	}

	return false
}
