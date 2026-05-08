package types

import "errors"

// ErrAbandonTxOut signals the signer should permanently remove the txout
// (no retry safe). Returned today by substrate on stale-nonce broadcasts;
// any future chainclient with similar semantics can return it as well.
var ErrAbandonTxOut = errors.New("abandon txout")
