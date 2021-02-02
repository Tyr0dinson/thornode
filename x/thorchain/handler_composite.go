package thorchain

import (
	"fmt"
	"sort"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// MintCompositeHandler is to process native messages on THORChain
type MintCompositeHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewMintCompositeHandler create a new instance of MintCompositeHandler
func NewMintCompositeHandler(keeper keeper.Keeper, mgr Manager) MintCompositeHandler {
	return MintCompositeHandler{
		keeper: keeper,
		mgr:    mgr,
	}
}

// Run is the main entry of MintCompositeHandler
func (h MintCompositeHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(*MsgMintComposite)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg, version); err != nil {
		ctx.Logger().Error("MsgMintComposite failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("fail to process MsgMintComposite", "error", err)
		return nil, err
	}
	return result, nil
}

func (h MintCompositeHandler) validate(ctx cosmos.Context, msg MsgMintComposite, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errInvalidVersion
}

func (h MintCompositeHandler) validateV1(ctx cosmos.Context, msg MsgMintComposite) error {
	return msg.ValidateBasic()
}

func (h MintCompositeHandler) handle(ctx cosmos.Context, msg MsgMintComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgMintComposite", "from", msg.GetSigners()[0], "coin", msg.Coin, "id", msg.CompositeId)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errInvalidVersion
}

func (h MintCompositeHandler) handleV1(ctx cosmos.Context, msg MsgMintComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// check account has appropriate funds
	transactionFee := h.mgr.GasMgr().GetFee(ctx, common.THORChain, common.RuneAsset())
	gas := common.NewCoin(common.RuneNative, transactionFee)
	gasFee, err := gas.Native()
	if err != nil {
		return nil, fmt.Errorf("fail to get gas fee: %w", err)
	}

	coin, err := msg.Coin.Native()
	if err != nil {
		return nil, ErrInternal(err, "coins are native to THORChain")
	}

	totalCoins := cosmos.NewCoins(gasFee).Add(coin)
	if !h.keeper.HasCoins(ctx, msg.GetSigners()[0], totalCoins) {
		return nil, cosmos.ErrInsufficientCoins(err, "insufficient funds")
	}

	// send gas to reserve
	err = h.keeper.SendFromAccountToModule(ctx, msg.GetSigners()[0], ReserveName, common.NewCoins(gas))
	if err != nil {
		return nil, fmt.Errorf("unable to send gas to reserve: %w", err)
	}

	comp, err := h.keeper.GetComposite(ctx, msg.CompositeId)
	if err != nil {
		return nil, err
	}
	if comp.IsEmpty() {
		return nil, fmt.Errorf("composite id %s is not found", msg.CompositeId)
	}

	totalCompSupply := h.keeper.GetTotalSupply(ctx, comp.Asset())
	totalRuneValue, err := h.mgr.CompositeManager().GetRuneValue(ctx, &comp)
	if err != nil {
		return nil, err
	}
	pool, err := h.keeper.GetPool(ctx, msg.Coin.Asset)
	if err != nil {
		return nil, err
	}
	depositValue := pool.RuneValueInAsset(msg.Coin.Amount)
	compToMint := common.GetShare(depositValue, totalRuneValue, totalCompSupply)
	if totalCompSupply.IsZero() {
		compToMint = depositValue
	}

	found := false // the composite holding must already hold this synthetic asset
	for i, coin := range comp.Holdings {
		if coin.Asset.Equals(msg.Coin.Asset) {
			comp.Holdings[i].Amount = comp.Holdings[i].Amount.Add(msg.Coin.Amount)
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("asset %s is not a current held by composite asset %s", msg.Coin.Asset, comp.Id)
	}

	if err := h.keeper.MintAndSendToAccount(ctx, msg.Signer, common.NewCoin(comp.Asset(), compToMint)); err != nil {
		return nil, ErrInternal(err, "fail to mint native rune coins")
	}

	if err := h.keeper.SetComposite(ctx, comp); err != nil {
		return nil, err
	}

	return &cosmos.Result{}, nil
}

// BurnCompositeHandler is to process native messages on THORChain
type BurnCompositeHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewBurnCompositeHandler create a new instance of BurnCompositeHandler
func NewBurnCompositeHandler(keeper keeper.Keeper, mgr Manager) BurnCompositeHandler {
	return BurnCompositeHandler{
		keeper: keeper,
		mgr:    mgr,
	}
}

// Run is the main entry of BurnCompositeHandler
func (h BurnCompositeHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBurnComposite)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg, version); err != nil {
		ctx.Logger().Error("MsgBurnComposite failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("fail to process MsgBurnComposite", "error", err)
		return nil, err
	}
	return result, nil
}

func (h BurnCompositeHandler) validate(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errInvalidVersion
}

func (h BurnCompositeHandler) validateV1(ctx cosmos.Context, msg MsgBurnComposite) error {
	return msg.ValidateBasic()
}

func (h BurnCompositeHandler) handle(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgBurnComposite", "from", msg.GetSigners()[0], "coin", msg.Coin)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errInvalidVersion
}

func (h BurnCompositeHandler) handleV1(ctx cosmos.Context, msg MsgBurnComposite, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// check account has appropriate funds
	transactionFee := h.mgr.GasMgr().GetFee(ctx, common.THORChain, common.RuneAsset())
	gas := common.NewCoin(common.RuneNative, transactionFee)
	gasFee, err := gas.Native()
	if err != nil {
		return nil, fmt.Errorf("fail to get gas fee: %w", err)
	}

	coin, err := msg.Coin.Native()
	if err != nil {
		return nil, ErrInternal(err, "coins are native to THORChain")
	}

	totalCoins := cosmos.NewCoins(gasFee).Add(coin)
	if !h.keeper.HasCoins(ctx, msg.GetSigners()[0], totalCoins) {
		return nil, cosmos.ErrInsufficientCoins(err, "insufficient funds")
	}

	// send gas to reserve
	err = h.keeper.SendFromAccountToModule(ctx, msg.GetSigners()[0], ReserveName, common.NewCoins(gas))
	if err != nil {
		return nil, fmt.Errorf("unable to send gas to reserve: %w", err)
	}

	id := msg.Coin.Asset.GetCompsiteId()
	comp, err := h.keeper.GetComposite(ctx, id)
	if err != nil {
		return nil, err
	}

	totalRuneValue, err := h.mgr.CompositeManager().GetRuneValue(ctx, &comp)
	if err != nil {
		return nil, err
	}
	totalCompSupply := h.keeper.GetTotalSupply(ctx, comp.Asset())
	burnRuneValue := common.GetShare(msg.Coin.Amount, totalCompSupply, totalRuneValue)

	pools := make([]Pool, len(comp.Holdings))
	for i, coin := range comp.Holdings {
		pools[i], err = h.keeper.GetPool(ctx, coin.Asset)
		if err != nil {
			return nil, err
		}
	}
	// sort by rune balance
	sort.Slice(pools[:], func(i, j int) bool {
		return pools[i].BalanceRune.GT(pools[j].BalanceRune)
	})

	// iterate over synth assets in composite to send user. Start with deepest pool first
	toSend := make(common.Coins, 0)
	for _, pool := range {
		holding := comp.Holdings.GetCoin(pool.Asset.GetSyntheticAsset())
		holdingValueInRune := pool.GetAssetValueInRune(holding.Amount)
		if holdingValueInRune.GT(burnRuneValue) {
		}
		toSend = toSend.Add(holding)
	}

	return &cosmos.Result{}, nil
}
