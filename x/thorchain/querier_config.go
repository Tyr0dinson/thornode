// querier_config.go contains all configuration and system-related query handlers.
// This includes queries for mimir values, constants, upgrades, and invariants.

package thorchain

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Constants and Version Queries ============

func (qs queryServer) queryConstantValues(_ cosmos.Context, _ *types.QueryConstantValuesRequest) (*types.QueryConstantValuesResponse, error) {
	constAccessor := qs.mgr.GetConstants()
	cv := constAccessor.GetConstantValsByKeyname()

	proto := types.QueryConstantValuesResponse{}
	// analyze-ignore(map-iteration)
	for k, v := range cv.Int64Values {
		proto.Int_64Values = append(proto.Int_64Values, &types.Int64Constants{
			Name:  k,
			Value: v,
		})
	}
	// analyze-ignore(map-iteration)
	for k, v := range cv.BoolValues {
		proto.BoolValues = append(proto.BoolValues, &types.BoolConstants{
			Name:  k,
			Value: v,
		})
	}
	// analyze-ignore(map-iteration)
	for k, v := range cv.StringValues {
		proto.StringValues = append(proto.StringValues, &types.StringConstants{
			Name:  k,
			Value: v,
		})
	}
	return &proto, nil
}

func (qs queryServer) queryVersion(ctx cosmos.Context, _ *types.QueryVersionRequest) (*types.QueryVersionResponse, error) {
	ver := qs.mgr.Keeper().GetLowestActiveVersion(ctx)
	versionString := ver.String()
	if versionString == "" {
		versionString = semver.MustParse("0.0.0").String()
	}
	nextVer, hasNext := qs.mgr.Keeper().GetVersionWithCtx(ctx)
	nextVersionString := ""
	if hasNext {
		nextVersionString = nextVer.String()
	}
	result := types.QueryVersionResponse{
		Current: versionString,
		Next:    nextVersionString,
		Querier: qs.mgr.GetVersion().String(),
	}
	return &result, nil
}

// ============ Mimir Queries ============

func (qs queryServer) queryMimirWithKey(ctx cosmos.Context, req *types.QueryMimirWithKeyRequest) (*types.QueryMimirWithKeyResponse, error) {
	if len(req.Key) == 0 {
		return nil, errors.New("key not provided")
	}

	v, err := qs.mgr.Keeper().GetMimir(ctx, req.Key)
	if err != nil {
		return nil, fmt.Errorf("fail to get mimir with key:%s, err:%w", req.Key, err)
	}
	return &types.QueryMimirWithKeyResponse{
		Value: v,
	}, nil
}

func (qs queryServer) queryMimirValues(ctx cosmos.Context, _ *types.QueryMimirValuesRequest) (*types.QueryMimirValuesResponse, error) {
	values, err := getMimirV2(ctx, qs.mgr.Keeper(), qs.mgr.GetVersion())
	if err != nil {
		return nil, err
	}
	mimirs := make([]*types.Mimir, 0, len(values))
	// analyze-ignore(map-iteration)
	for k, v := range values {
		val, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		mimirs = append(mimirs, &types.Mimir{
			Key:   k,
			Value: val,
		})
	}
	return &types.QueryMimirValuesResponse{Mimirs: mimirs}, nil
}

func (qs queryServer) queryMimirAdminValues(ctx cosmos.Context, _ *types.QueryMimirAdminValuesRequest) (*types.QueryMimirAdminValuesResponse, error) {
	allKeys := qs.mgr.Keeper().GetAllMimirKeys(ctx)
	// analyze-ignore(map-iteration)
	for k := range constants.GetConstantValues(qs.mgr.GetVersion()) {
		allKeys = append(allKeys, strings.ToUpper(k))
	}
	res := make(map[string]int64)
	for _, key := range allKeys {
		v, err := qs.mgr.Keeper().GetAdminMimir(ctx, key)
		if err != nil {
			continue
		}
		res[key] = v
	}
	return &types.QueryMimirAdminValuesResponse{MimirValues: res}, nil
}

func (qs queryServer) queryMimirNodesAllValues(ctx cosmos.Context, _ *types.QueryMimirNodesAllValuesRequest) (*types.QueryMimirNodesAllValuesResponse, error) {
	values := make(map[string]map[string]int64)
	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var nodeMimir NodeMimir
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &nodeMimir); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		if nodeMimir.Mimir == "" {
			continue
		}
		n := nodeMimir.Signer.String()
		if _, ok := values[nodeMimir.Mimir]; !ok {
			values[nodeMimir.Mimir] = make(map[string]int64)
		}
		values[nodeMimir.Mimir][n] = nodeMimir.Value
	}
	return &types.QueryMimirNodesAllValuesResponse{Values: values}, nil
}

func (qs queryServer) queryMimirNodesValues(ctx cosmos.Context, _ *types.QueryMimirNodesValuesRequest) (*types.QueryMimirNodesValuesResponse, error) {
	values := make(map[string]int64)
	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var nodeMimir NodeMimir
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &nodeMimir); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		if nodeMimir.Mimir == "" {
			continue
		}
		values[nodeMimir.Mimir] = values[nodeMimir.Mimir] + 1
	}
	// iterate over all keys in the map and find the keys that have at least 2/3rds
	var mimirs = make(map[string]int64)
	numberOfActiveNodes, err := qs.mgr.Keeper().GetNodeAccountsActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get number of active nodes: %w", err)
	}
	var data []string
	for key := range values {
		data = append(data, key)
	}
	sort.Strings(data)
	for _, key := range data {
		value := values[key]
		if HasSuperMajority(int(value), numberOfActiveNodes) {
			mimirs[key] = value
		}
	}
	return &types.QueryMimirNodesValuesResponse{MimirValues: mimirs}, nil
}

func (qs queryServer) queryMimirNodeValues(ctx cosmos.Context, req *types.QueryMimirNodeValuesRequest) (*types.QueryMimirNodeValuesResponse, error) {
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, fmt.Errorf("fail to parse thor address, err: %w", err)
	}
	values := make(map[string]int64)
	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var nodeMimir NodeMimir
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &nodeMimir); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		if nodeMimir.Mimir == "" {
			continue
		}
		if !nodeMimir.Signer.Equals(addr) {
			continue
		}
		values[nodeMimir.Mimir] = nodeMimir.Value
	}
	return &types.QueryMimirNodeValuesResponse{MimirValues: values}, nil
}

// ============ Upgrade Queries ============

func (qs queryServer) queryUpgradeProposals(ctx cosmos.Context, req *types.QueryUpgradeProposalsRequest) (*types.QueryUpgradeProposalsResponse, error) {
	// parse and validate both arguments
	startHeight, err := strconv.ParseInt(req.StartHeight, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fail to parse start height: %w", err)
	}
	endHeight, err := strconv.ParseInt(req.EndHeight, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fail to parse end height: %w", err)
	}
	if startHeight >= endHeight {
		return nil, fmt.Errorf("start height must be less than end height")
	}

	items := qs.mgr.Keeper().GetAllUpgradeProposals(ctx, startHeight, endHeight)
	proposals := make([]*types.UpgradeProposal, len(items))
	for i := range items {
		proposals[i] = &items[i]
	}
	return &types.QueryUpgradeProposalsResponse{Proposals: proposals}, nil
}

func (qs queryServer) queryUpgradeProposal(ctx cosmos.Context, req *types.QueryUpgradeProposalRequest) (*types.QueryUpgradeProposalResponse, error) {
	height, err := strconv.ParseInt(req.Height, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fail to parse height: %w", err)
	}
	key := fmt.Sprintf("%s-%s", req.Name, req.Height)
	up, err := qs.mgr.Keeper().GetUpgradeProposal(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("fail to get upgrade proposal: %w", err)
	}
	total, err := qs.mgr.Keeper().GetNodeAccountsActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get active node count: %w", err)
	}
	result := types.QueryUpgradeProposalResponse{
		Proposal: &up,
		Voted:    HasSuperMajority(len(up.GetApprovers()), total),
		Height:   height,
	}
	return &result, nil
}

func (qs queryServer) queryUpgradeVotes(ctx cosmos.Context, req *types.QueryUpgradeVotesRequest) (*types.QueryUpgradeVotesResponse, error) {
	key := fmt.Sprintf("%s-%s", req.Name, req.Height)
	up, err := qs.mgr.Keeper().GetUpgradeProposal(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("fail to get upgrade proposal: %w", err)
	}
	total, err := qs.mgr.Keeper().GetNodeAccountsActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get active node count: %w", err)
	}
	result := types.QueryUpgradeVotesResponse{}
	for i := range up.GetApprovers() {
		result.Votes = append(result.Votes, up.GetApprovers()[i].String())
	}
	result.Total = strconv.Itoa(total)
	return &result, nil
}

// ============ Invariant Queries ============

func (qs queryServer) queryInvariants(_ cosmos.Context, _ *types.QueryInvariantsRequest) (*types.QueryInvariantsResponse, error) {
	result := types.QueryInvariantsResponse{
		Invariants: []types.QueryInvariant{
			"asgard", "bond", "pools", "runepool", "thorname",
		},
	}
	return &result, nil
}

func (qs queryServer) queryInvariant(ctx cosmos.Context, req *types.QueryInvariantRequest) (*types.QueryInvariantResponse, error) {
	if len(req.Invariant) == 0 {
		return nil, errors.New("invalid request")
	}
	invariant := strings.ToLower(req.Invariant)
	result := types.QueryInvariantResponse{}
	switch invariant {
	case "asgard":
		result.Invariant = qs.mgr.AsgardKeeper().AsgardInvariant(ctx)
		result.Msg = result.Invariant.Msg
	case "bond":
		result.Invariant = qs.mgr.ValidatorKeeper().BondInvariant(ctx)
		result.Msg = result.Invariant.Msg
	case "pools":
		result.Invariant = qs.mgr.PoolKeeper().PoolInvariant(ctx)
		result.Msg = result.Invariant.Msg
	case "runepool":
		result.Invariant = qs.mgr.RUNEPoolKeeper().RunePoolInvariant(ctx)
		result.Msg = result.Invariant.Msg
	case "thorname":
		result.Invariant = qs.mgr.AffiliateKeeper().THORNameInvariant(ctx)
		result.Msg = result.Invariant.Msg
	default:
		return nil, fmt.Errorf("invalid invariant: %s", req.Invariant)
	}
	return &result, nil
}

// ============ Helper Functions ============

func getMimirV2(ctx cosmos.Context, keeper keeper.Keeper, version semver.Version) (map[string]string, error) {
	// check all node vote mimir value, and computer 2/3 majority consensus
	voteMimirs := make(map[string]map[string]int64)
	iter := keeper.GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var nodeMimir NodeMimir
		if err := keeper.Cdc().Unmarshal(iter.Value(), &nodeMimir); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		k := strings.ToUpper(nodeMimir.Mimir)
		if _, ok := voteMimirs[k]; !ok {
			voteMimirs[k] = make(map[string]int64)
		}
		voteMimirs[k][strconv.FormatInt(nodeMimir.Value, 10)]++
	}

	actualMimirs := make(map[string]int64)
	numberOfActiveNodes, err := keeper.GetNodeAccountsActiveCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get number of active nodes : %w", err)
	}
	// analyze-ignore(map-iteration)
	for k, tallyValue := range voteMimirs {
		var thresholdMet bool
		// analyze-ignore(map-iteration)
		for mValue, cnt := range tallyValue {
			if HasMinority(int(cnt), numberOfActiveNodes) {
				continue
			}
			if HasSuperMajority(int(cnt), numberOfActiveNodes) || thresholdMet {
				v, err := strconv.ParseInt(mValue, 10, 64)
				if err != nil {
					ctx.Logger().Error("fail to parse mimir value", "error", err)
					continue
				}
				actualMimirs[k] = v
				thresholdMet = true
			}
		}
	}
	constAccessor := constants.GetConstantValues(version)
	allMimirKeys := keeper.GetAllMimirKeys(ctx)

	// mimir set by admin takes precedence
	adminMimirValue := make(map[string]int64)
	// get all mimir keys , admin mimir can overwrite any mimir
	// analyze-ignore(map-iteration)
	for k := range constAccessor {
		allMimirKeys = append(allMimirKeys, strings.ToUpper(k))
	}
	for _, item := range allMimirKeys {
		if v, err := keeper.GetAdminMimir(ctx, item); err == nil {
			adminMimirValue[item] = v
		}
	}

	// merge constant values
	res := make(map[string]string)
	// analyze-ignore(map-iteration)
	for k, v := range constAccessor {
		mKey := strings.ToUpper(k)
		if av, ok := adminMimirValue[mKey]; ok {
			v = av
		} else if av, ok := actualMimirs[mKey]; ok {
			v = av
		}
		res[k] = strconv.FormatInt(v, 10)
	}

	// if admin set mimir on a key which doesn't exist in constants , thus didn't set in the
	// above loop , set it here
	// analyze-ignore(map-iteration)
	for k, v := range adminMimirValue {
		key := strings.ToUpper(k)
		if _, ok := res[k]; !ok {
			res[key] = strconv.FormatInt(v, 10)
		}
	}

	// if nodes vote on a key which doesn't exist in constants, thus didn't set it in
	// the above loop, set it here
	// analyze-ignore(map-iteration)
	for k, v := range actualMimirs {
		key := strings.ToUpper(k)
		if _, ok := res[key]; !ok {
			res[key] = strconv.FormatInt(v, 10)
		}
	}

	return res, nil
}

