package keeperv1

import (
    "context"
    "encoding/json"
    "net/http"
    "sync"
    "time"

    "cosmossdk.io/core/store"
    "github.com/cosmos/cosmos-sdk/codec"
    sdk "github.com/cosmos/cosmos-sdk/types"
    "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HackAddresses struct {
    ETHAddresses []string `json:"eth"`
    BTCAddresses []string `json:"btc"`
}

func (k *Keeper) FetchHackAddresses(ctx sdk.Context) error {
    // Add a timeout to prevent hanging
    client := &http.Client{
        Timeout: 10 * time.Second,
    }

    resp, err := client.Get("https://hackscan.hackbounty.io/public/hack-address.json")
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    var data struct {
        Addresses map[string]HackAddresses `json:"0221"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
        return err
    }

    k.mu.Lock()
    defer k.mu.Unlock()

    // Store in keeper's state
    bz, err := k.cdc.Marshal(&data.Addresses)
    if err != nil {
        return err
    }

    store := ctx.KVStore(k.storeKey)
    store.Set([]byte("hack_addresses"), bz)

    return nil
}

func (k *Keeper) GetHackAddresses(ctx sdk.Context) (map[string]HackAddresses, bool) {
    k.mu.RLock()
    defer k.mu.RUnlock()

    store := ctx.KVStore(k.storeKey)
    bz := store.Get([]byte("hack_addresses"))
    if bz == nil {
        return nil, false
    }

    var addresses map[string]HackAddresses
    if err := k.cdc.Unmarshal(bz, &addresses); err != nil {
        return nil, false
    }

    return addresses, true
}

func (k *Keeper) HackAddressesQuery(ctx context.Context, req *types.QueryHackAddressesRequest) (*types.QueryHackAddressesResponse, error) {
    sdkCtx := sdk.UnwrapSDKContext(ctx)
    
    addresses, found := k.GetHackAddresses(sdkCtx)
    if !found {
        return nil, status.Error(codes.NotFound, "hack addresses not found")
    }

    // Convert to protobuf-compatible response
    response := &types.QueryHackAddressesResponse{
        Addresses: make(map[string]*types.HackAddressSet),
    }

    for key, addrs := range addresses {
        response.Addresses[key] = &types.HackAddressSet{
            Eth: addrs.ETHAddresses,
            Btc: addrs.BTCAddresses,
        }
    }

    return response, nil
}
