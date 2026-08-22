package observer

import "gitlab.com/thorchain/thornode/v3/common"

// applySpentRefsAsync is the test-only semantic-memo convenience path. Live
// ingress always supplies an authenticated wire memo or resolves it from the
// committed THORChain plan.
func (s *AttestationGossip) applySpentRefsAsync(
	tx common.ObservedTx,
	onOutcome func(applied bool),
) {
	wireMemo := tx.Tx.Memo
	s.applySpentRefsAsyncWithLimit(tx, &wireMemo, onOutcome, s.applySpentRefsSem, "pre-quorum")
}

// SetObserverApplySpentOutputRefs adapts concise test callbacks to the sole
// production applier contract. Production always resolves the authenticated
// wire memo and lets the client acquire the guard for every retry attempt.
func (s *AttestationGossip) SetObserverApplySpentOutputRefs(
	fn func(tx common.ObservedTx, onOutcome func(applied bool)),
) {
	if fn == nil {
		s.SetObserverApplySpentOutputRefsGuarded(nil)
		return
	}
	s.SetObserverApplySpentOutputRefsGuarded(func(
		tx common.ObservedTx,
		_ string,
		guard func() (func(), bool),
		onOutcome func(bool),
	) {
		release, ok := guard()
		if !ok {
			if onOutcome != nil {
				onOutcome(false)
			}
			return
		}
		defer release()
		fn(tx, onOutcome)
	})
}

// SetObserverApplySpentOutputRefsWithWireMemo adapts wire-aware test callbacks
// without retaining a second production configuration path.
func (s *AttestationGossip) SetObserverApplySpentOutputRefsWithWireMemo(
	fn func(tx common.ObservedTx, wireMemo string, onOutcome func(applied bool)),
) {
	if fn == nil {
		s.SetObserverApplySpentOutputRefsGuarded(nil)
		return
	}
	s.SetObserverApplySpentOutputRefsGuarded(func(
		tx common.ObservedTx,
		wireMemo string,
		guard func() (func(), bool),
		onOutcome func(bool),
	) {
		release, ok := guard()
		if !ok {
			if onOutcome != nil {
				onOutcome(false)
			}
			return
		}
		defer release()
		fn(tx, wireMemo, onOutcome)
	})
}
