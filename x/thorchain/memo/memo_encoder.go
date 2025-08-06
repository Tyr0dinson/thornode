package thorchain

import (
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// MemoEncoder provides functionality to encode various THORChain transaction types into memo strings
type MemoEncoder struct{}

// NewMemoEncoder creates a new MemoEncoder instance
func NewMemoEncoder() *MemoEncoder {
	return &MemoEncoder{}
}

// SwapMemoParams contains parameters for swap memo encoding
type SwapMemoParams struct {
	TargetAsset           common.Asset
	DestinationAddress    common.Address
	SlipLimit             cosmos.Uint
	AffiliateAddress      common.Address
	AffiliateBasisPoints  cosmos.Uint
	DexAggregator         string
	DexTargetAddress      string
	DexTargetLimit        *cosmos.Uint
	StreamQuantity        uint64
	StreamInterval        uint64
	RefundAddress         common.Address
}

// AddLiquidityMemoParams contains parameters for add liquidity memo encoding
type AddLiquidityMemoParams struct {
	Asset               common.Asset
	SymmetricAddress    common.Address
	AffiliateAddress    common.Address
	AffiliateBasisPoints cosmos.Uint
}

// WithdrawMemoParams contains parameters for withdraw memo encoding
type WithdrawMemoParams struct {
	Asset               common.Asset
	WithdrawBasisPoints cosmos.Uint
	TargetAsset         common.Asset
	AffiliateAddress    common.Address
	AffiliateBasisPoints cosmos.Uint
}

// BondMemoParams contains parameters for bond memo encoding
type BondMemoParams struct {
	NodeAddress         common.Address
	BondProviderAddress common.Address
	OperatorFee         int64
}

// EncodeSwapMemo encodes a swap transaction memo
func (e *MemoEncoder) EncodeSwapMemo(params SwapMemoParams) (string, error) {
	if params.TargetAsset.IsEmpty() {
		return "", fmt.Errorf("target asset cannot be empty")
	}

	// Basic swap memo format: SWAP:ASSET:DEST:LIMIT
	memo := fmt.Sprintf("SWAP:%s", params.TargetAsset.String())

	// Add destination if provided
	if !params.DestinationAddress.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, params.DestinationAddress.String())
	} else {
		memo = fmt.Sprintf("%s:", memo) // Empty destination
	}

	// Add slip limit if provided
	if !params.SlipLimit.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, params.SlipLimit.String())
	} else {
		memo = fmt.Sprintf("%s:", memo) // Empty slip limit
	}

	// Add affiliate info if provided
	if !params.AffiliateAddress.IsEmpty() && !params.AffiliateBasisPoints.IsZero() {
		memo = fmt.Sprintf("%s:%s:%s", memo, params.AffiliateAddress.String(), params.AffiliateBasisPoints.String())
	} else if !params.AffiliateAddress.IsEmpty() {
		memo = fmt.Sprintf("%s:%s:", memo, params.AffiliateAddress.String())
	}

	// Add DEX aggregator info if provided
	if params.DexAggregator != "" && params.DexTargetAddress != "" {
		memo = fmt.Sprintf("%s:%s:%s", memo, params.DexAggregator, params.DexTargetAddress)
		if params.DexTargetLimit != nil && !params.DexTargetLimit.IsZero() {
			memo = fmt.Sprintf("%s:%s", memo, params.DexTargetLimit.String())
		}
	}

	// Add streaming swap info if provided
	if params.StreamInterval > 0 {
		memo = fmt.Sprintf("%s/%d/%d", memo, params.StreamQuantity, params.StreamInterval)
	}

	return memo, nil
}

// EncodeAddLiquidityMemo encodes an add liquidity transaction memo
func (e *MemoEncoder) EncodeAddLiquidityMemo(params AddLiquidityMemoParams) (string, error) {
	if params.Asset.IsEmpty() {
		return "", fmt.Errorf("asset cannot be empty")
	}

	// Basic add liquidity memo format: ADD:ASSET:ADDR
	memo := fmt.Sprintf("ADD:%s", params.Asset.String())

	// Add symmetric address if provided (for dual-sided adds)
	if !params.SymmetricAddress.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, params.SymmetricAddress.String())
	} else {
		memo = fmt.Sprintf("%s:", memo) // Empty for single-sided
	}

	// Add affiliate info if provided
	if !params.AffiliateAddress.IsEmpty() && !params.AffiliateBasisPoints.IsZero() {
		memo = fmt.Sprintf("%s:%s:%s", memo, params.AffiliateAddress.String(), params.AffiliateBasisPoints.String())
	}

	return memo, nil
}

// EncodeWithdrawMemo encodes a withdraw liquidity transaction memo
func (e *MemoEncoder) EncodeWithdrawMemo(params WithdrawMemoParams) (string, error) {
	if params.Asset.IsEmpty() {
		return "", fmt.Errorf("asset cannot be empty")
	}

	// Basic withdraw memo format: WITHDRAW:ASSET:BASIS_POINTS
	memo := fmt.Sprintf("WITHDRAW:%s", params.Asset.String())

	// Add withdraw basis points
	if !params.WithdrawBasisPoints.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, params.WithdrawBasisPoints.String())
	} else {
		memo = fmt.Sprintf("%s:10000", memo) // Default to 100% withdrawal
	}

	// Add target asset for asymmetric withdrawals
	if !params.TargetAsset.IsEmpty() && !params.TargetAsset.Equals(params.Asset) {
		memo = fmt.Sprintf("%s:%s", memo, params.TargetAsset.String())
	}

	// Add affiliate info if provided
	if !params.AffiliateAddress.IsEmpty() && !params.AffiliateBasisPoints.IsZero() {
		memo = fmt.Sprintf("%s:%s:%s", memo, params.AffiliateAddress.String(), params.AffiliateBasisPoints.String())
	}

	return memo, nil
}

// EncodeBondMemo encodes a bond transaction memo
func (e *MemoEncoder) EncodeBondMemo(params BondMemoParams) (string, error) {
	if params.NodeAddress.IsEmpty() {
		return "", fmt.Errorf("node address cannot be empty")
	}

	// Basic bond memo format: BOND:NODE_ADDR
	memo := fmt.Sprintf("BOND:%s", params.NodeAddress.String())

	// Add bond provider address if different from node address
	if !params.BondProviderAddress.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, params.BondProviderAddress.String())
	}

	// Add operator fee if provided
	if params.OperatorFee > 0 {
		memo = fmt.Sprintf("%s:%d", memo, params.OperatorFee)
	}

	return memo, nil
}

// EncodeUnbondMemo encodes an unbond transaction memo
func (e *MemoEncoder) EncodeUnbondMemo(nodeAddress common.Address, unbondAmount cosmos.Uint, bondProviderAddress common.Address) (string, error) {
	if nodeAddress.IsEmpty() {
		return "", fmt.Errorf("node address cannot be empty")
	}

	memo := fmt.Sprintf("UNBOND:%s", nodeAddress.String())

	if !unbondAmount.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, unbondAmount.String())
	}

	if !bondProviderAddress.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, bondProviderAddress.String())
	}

	return memo, nil
}

// EncodeLeaveMemo encodes a leave transaction memo
func (e *MemoEncoder) EncodeLeaveMemo(nodeAddress common.Address) (string, error) {
	if nodeAddress.IsEmpty() {
		return "", fmt.Errorf("node address cannot be empty")
	}

	return fmt.Sprintf("LEAVE:%s", nodeAddress.String()), nil
}

// EncodeDonate encodes a donate transaction memo
func (e *MemoEncoder) EncodeDonate(asset common.Asset) (string, error) {
	if asset.IsEmpty() {
		return "", fmt.Errorf("asset cannot be empty")
	}

	return fmt.Sprintf("DONATE:%s", asset.String()), nil
}

// EncodeRefundMemo encodes a refund transaction memo
func (e *MemoEncoder) EncodeRefundMemo(txID common.TxID) (string, error) {
	if txID.IsEmpty() {
		return "", fmt.Errorf("transaction ID cannot be empty")
	}

	return fmt.Sprintf("REFUND:%s", txID.String()), nil
}

// EncodeLimitSwapMemo encodes a limit swap transaction memo
func (e *MemoEncoder) EncodeLimitSwapMemo(targetAsset common.Asset, destAddr common.Address, tradeTarget cosmos.Uint) (string, error) {
	if targetAsset.IsEmpty() {
		return "", fmt.Errorf("target asset cannot be empty")
	}

	memo := fmt.Sprintf("=:%s", targetAsset.String())

	if !destAddr.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, destAddr.String())
	} else {
		memo = fmt.Sprintf("%s:", memo)
	}

	if !tradeTarget.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, tradeTarget.String())
	}

	return memo, nil
}

// EncodeLoanOpenMemo encodes a loan open transaction memo
func (e *MemoEncoder) EncodeLoanOpenMemo(targetAsset common.Asset, destAddr common.Address, minOut cosmos.Uint, affAddr common.Address, affPts cosmos.Uint) (string, error) {
	if targetAsset.IsEmpty() {
		return "", fmt.Errorf("target asset cannot be empty")
	}

	memo := fmt.Sprintf("$+:%s", targetAsset.String())

	if !destAddr.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, destAddr.String())
	} else {
		memo = fmt.Sprintf("%s:", memo)
	}

	if !minOut.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, minOut.String())
	}

	if !affAddr.IsEmpty() && !affPts.IsZero() {
		memo = fmt.Sprintf("%s:%s:%s", memo, affAddr.String(), affPts.String())
	}

	return memo, nil
}

// EncodeLoanRepaymentMemo encodes a loan repayment transaction memo
func (e *MemoEncoder) EncodeLoanRepaymentMemo(asset common.Asset, ownerAddr common.Address, minOut cosmos.Uint) (string, error) {
	if asset.IsEmpty() {
		return "", fmt.Errorf("asset cannot be empty")
	}

	memo := fmt.Sprintf("$-:%s", asset.String())

	if !ownerAddr.IsEmpty() {
		memo = fmt.Sprintf("%s:%s", memo, ownerAddr.String())
	}

	if !minOut.IsZero() {
		memo = fmt.Sprintf("%s:%s", memo, minOut.String())
	}

	return memo, nil
}

// EncodeGenericMemo encodes a generic memo based on transaction type and parameters
func (e *MemoEncoder) EncodeGenericMemo(txType TxType, params map[string]interface{}) (string, error) {
	switch txType {
	case TxSwap:
		return e.encodeGenericSwap(params)
	case TxAdd:
		return e.encodeGenericAdd(params)
	case TxWithdraw:
		return e.encodeGenericWithdraw(params)
	case TxBond:
		return e.encodeGenericBond(params)
	case TxUnbond:
		return e.encodeGenericUnbond(params)
	case TxDonate:
		return e.encodeGenericDonate(params)
	case TxLeave:
		return e.encodeGenericLeave(params)
	case TxRefund:
		return e.encodeGenericRefund(params)
	default:
		return "", fmt.Errorf("unsupported transaction type: %v", txType)
	}
}

// Helper methods for generic encoding

func (e *MemoEncoder) encodeGenericSwap(params map[string]interface{}) (string, error) {
	targetAsset, ok := params["target_asset"].(string)
	if !ok || targetAsset == "" {
		return "", fmt.Errorf("target_asset required for swap")
	}

	asset, err := common.NewAsset(targetAsset)
	if err != nil {
		return "", fmt.Errorf("invalid target asset: %w", err)
	}

	swapParams := SwapMemoParams{
		TargetAsset: asset,
	}

	// Optional parameters
	if destAddr, ok := params["destination"].(string); ok && destAddr != "" {
		addr, err := common.NewAddress(destAddr)
		if err != nil {
			return "", fmt.Errorf("invalid destination address: %w", err)
		}
		swapParams.DestinationAddress = addr
	}

	if slipStr, ok := params["slip_limit"].(string); ok && slipStr != "" {
		slip, err := cosmos.ParseUint(slipStr)
		if err != nil {
			return "", fmt.Errorf("invalid slip limit: %w", err)
		}
		swapParams.SlipLimit = slip
	}

	if affAddr, ok := params["affiliate_address"].(string); ok && affAddr != "" {
		addr, err := common.NewAddress(affAddr)
		if err != nil {
			return "", fmt.Errorf("invalid affiliate address: %w", err)
		}
		swapParams.AffiliateAddress = addr
	}

	if affBpsStr, ok := params["affiliate_bps"].(string); ok && affBpsStr != "" {
		bps, err := cosmos.ParseUint(affBpsStr)
		if err != nil {
			return "", fmt.Errorf("invalid affiliate basis points: %w", err)
		}
		swapParams.AffiliateBasisPoints = bps
	}

	return e.EncodeSwapMemo(swapParams)
}

func (e *MemoEncoder) encodeGenericAdd(params map[string]interface{}) (string, error) {
	assetStr, ok := params["asset"].(string)
	if !ok || assetStr == "" {
		return "", fmt.Errorf("asset required for add liquidity")
	}

	asset, err := common.NewAsset(assetStr)
	if err != nil {
		return "", fmt.Errorf("invalid asset: %w", err)
	}

	addParams := AddLiquidityMemoParams{
		Asset: asset,
	}

	// Optional symmetric address
	if symAddr, ok := params["symmetric_address"].(string); ok && symAddr != "" {
		addr, err := common.NewAddress(symAddr)
		if err != nil {
			return "", fmt.Errorf("invalid symmetric address: %w", err)
		}
		addParams.SymmetricAddress = addr
	}

	return e.EncodeAddLiquidityMemo(addParams)
}

func (e *MemoEncoder) encodeGenericWithdraw(params map[string]interface{}) (string, error) {
	assetStr, ok := params["asset"].(string)
	if !ok || assetStr == "" {
		return "", fmt.Errorf("asset required for withdraw")
	}

	asset, err := common.NewAsset(assetStr)
	if err != nil {
		return "", fmt.Errorf("invalid asset: %w", err)
	}

	withdrawParams := WithdrawMemoParams{
		Asset: asset,
	}

	// Optional withdraw percentage
	if bpsStr, ok := params["basis_points"].(string); ok && bpsStr != "" {
		bps, err := cosmos.ParseUint(bpsStr)
		if err != nil {
			return "", fmt.Errorf("invalid basis points: %w", err)
		}
		withdrawParams.WithdrawBasisPoints = bps
	}

	return e.EncodeWithdrawMemo(withdrawParams)
}

func (e *MemoEncoder) encodeGenericBond(params map[string]interface{}) (string, error) {
	nodeAddr, ok := params["node_address"].(string)
	if !ok || nodeAddr == "" {
		return "", fmt.Errorf("node_address required for bond")
	}

	addr, err := common.NewAddress(nodeAddr)
	if err != nil {
		return "", fmt.Errorf("invalid node address: %w", err)
	}

	bondParams := BondMemoParams{
		NodeAddress: addr,
	}

	return e.EncodeBondMemo(bondParams)
}

func (e *MemoEncoder) encodeGenericUnbond(params map[string]interface{}) (string, error) {
	nodeAddr, ok := params["node_address"].(string)
	if !ok || nodeAddr == "" {
		return "", fmt.Errorf("node_address required for unbond")
	}

	addr, err := common.NewAddress(nodeAddr)
	if err != nil {
		return "", fmt.Errorf("invalid node address: %w", err)
	}

	var amount cosmos.Uint
	if amountStr, ok := params["amount"].(string); ok && amountStr != "" {
		parsed, err := cosmos.ParseUint(amountStr)
		if err != nil {
			return "", fmt.Errorf("invalid unbond amount: %w", err)
		}
		amount = parsed
	}

	var bondProviderAddr common.Address
	if bpAddr, ok := params["bond_provider_address"].(string); ok && bpAddr != "" {
		parsed, err := common.NewAddress(bpAddr)
		if err != nil {
			return "", fmt.Errorf("invalid bond provider address: %w", err)
		}
		bondProviderAddr = parsed
	}

	return e.EncodeUnbondMemo(addr, amount, bondProviderAddr)
}

func (e *MemoEncoder) encodeGenericDonate(params map[string]interface{}) (string, error) {
	assetStr, ok := params["asset"].(string)
	if !ok || assetStr == "" {
		return "", fmt.Errorf("asset required for donate")
	}

	asset, err := common.NewAsset(assetStr)
	if err != nil {
		return "", fmt.Errorf("invalid asset: %w", err)
	}

	return e.EncodeDonate(asset)
}

func (e *MemoEncoder) encodeGenericLeave(params map[string]interface{}) (string, error) {
	nodeAddr, ok := params["node_address"].(string)
	if !ok || nodeAddr == "" {
		return "", fmt.Errorf("node_address required for leave")
	}

	addr, err := common.NewAddress(nodeAddr)
	if err != nil {
		return "", fmt.Errorf("invalid node address: %w", err)
	}

	return e.EncodeLeaveMemo(addr)
}

func (e *MemoEncoder) encodeGenericRefund(params map[string]interface{}) (string, error) {
	txIDStr, ok := params["tx_id"].(string)
	if !ok || txIDStr == "" {
		return "", fmt.Errorf("tx_id required for refund")
	}

	txID, err := common.NewTxID(txIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid transaction ID: %w", err)
	}

	return e.EncodeRefundMemo(txID)
}

// ValidateEncodedMemo validates that an encoded memo is properly formatted
func (e *MemoEncoder) ValidateEncodedMemo(memo string) error {
	if len(memo) == 0 {
		return fmt.Errorf("memo cannot be empty")
	}

	// Check maximum length
	if len(memo) > 1024 {
		return fmt.Errorf("memo too long (max 1024 characters)")
	}

	// Split into parts
	parts := strings.Split(memo, ":")
	if len(parts) == 0 {
		return fmt.Errorf("memo must have at least one part")
	}

	// Validate transaction type
	txType := strings.ToUpper(strings.TrimSpace(parts[0]))
	if _, err := StringToTxType(txType); err != nil {
		return fmt.Errorf("invalid transaction type: %s", txType)
	}

	return nil
}

// GetMemoDescription returns a human-readable description of a memo
func (e *MemoEncoder) GetMemoDescription(memo string) (string, error) {
	if len(memo) == 0 {
		return "", fmt.Errorf("memo cannot be empty")
	}

	parts := strings.Split(memo, ":")
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid memo format")
	}

	txType := strings.ToUpper(strings.TrimSpace(parts[0]))
	
	switch txType {
	case "SWAP", "=":
		if len(parts) > 1 {
			return fmt.Sprintf("Swap to %s", parts[1]), nil
		}
		return "Swap transaction", nil
	case "ADD", "+":
		if len(parts) > 1 {
			return fmt.Sprintf("Add liquidity to %s pool", parts[1]), nil
		}
		return "Add liquidity transaction", nil
	case "WITHDRAW", "WD", "-":
		if len(parts) > 1 {
			return fmt.Sprintf("Withdraw from %s pool", parts[1]), nil
		}
		return "Withdraw liquidity transaction", nil
	case "BOND":
		return "Bond to become validator", nil
	case "UNBOND":
		return "Unbond from validator", nil
	case "LEAVE":
		return "Leave validator set", nil
	case "DONATE", "D":
		if len(parts) > 1 {
			return fmt.Sprintf("Donate to %s pool", parts[1]), nil
		}
		return "Donation transaction", nil
	case "REFUND":
		return "Refund transaction", nil
	case "$+", "LOAN+":
		return "Open loan position", nil
	case "$-", "LOAN-":
		return "Repay loan", nil
	default:
		return fmt.Sprintf("Unknown transaction type: %s", txType), nil
	}
}