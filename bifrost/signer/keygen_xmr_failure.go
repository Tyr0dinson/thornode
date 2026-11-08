package signer

import (
	"errors"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type xmrKeygenFailure struct {
	cause  error
	absent []string
}

func (e *xmrKeygenFailure) Error() string { return e.cause.Error() }
func (e *xmrKeygenFailure) Unwrap() error { return e.cause }

// recordAbsence intersects local admission evidence across sequential attempts.
// Once a member participated, later aborts cannot turn it into a whole-ceremony
// absentee. The single monovault slot bounds retention to the latest ceremony.
func (s *xmrKeygenSlot) recordAbsence(height int64, keygen ttypes.Keygen, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var failure *xmrKeygenFailure
	candidates := make(map[string]bool)
	if errors.As(err, &failure) {
		for _, member := range failure.absent {
			candidates[strings.ToLower(strings.TrimSpace(member))] = true
		}
	}
	if s.absenceHeight != height || !s.absenceID.Equals(keygen.ID) {
		s.absenceHeight, s.absenceID = height, keygen.ID
		s.absent = append([]string(nil), keygen.Members...)
	}
	retained := make([]string, 0, len(s.absent))
	for _, member := range s.absent {
		if candidates[strings.ToLower(strings.TrimSpace(member))] {
			retained = append(retained, member)
		}
	}
	s.absent = retained
	if err == nil {
		return nil
	}
	return &xmrKeygenFailure{cause: err, absent: append([]string(nil), retained...)}
}

// A duplicate delivery may have started a newer attempt while this retry
// waited. Its owner will report its own terminal result; stale retained
// evidence must not race that work or a newer ceremony.
func (s *Signer) reportRetiredXMRKeygenFailure(retry *keygenRetry) {
	if retry.keygen.Type != ttypes.KeygenType_XMRMonovaultKeygen || retry.xmrFailure == nil {
		return
	}
	s.xmrKeygen.mu.Lock()
	current := s.xmrKeygen.active == nil &&
		s.xmrKeygen.latestHeight <= retry.block.Height &&
		s.xmrKeygen.absenceHeight == retry.block.Height &&
		s.xmrKeygen.absenceID.Equals(retry.keygen.ID)
	if !current {
		s.xmrKeygen.mu.Unlock()
		s.logger.Debug().Int64("height", retry.block.Height).
			Msg("skipping retired XMR failure report while another attempt owns admission or evidence changed")
		return
	}
	absent := append([]string(nil), s.xmrKeygen.absent...)
	// Reserve the existing serial slot through broadcast. A same-ceremony
	// delivery cannot start a retry between the check above and submission.
	s.xmrKeygen.active = &xmrKeygenRequest{height: retry.block.Height, keygen: retry.keygen, retry: true}
	s.xmrKeygen.latestHeight, s.xmrKeygen.latestID = retry.block.Height, retry.keygen.ID
	s.xmrKeygen.mu.Unlock()
	s.reportXMRKeygenFailure(retry.block, retry.keygen, &xmrKeygenFailure{cause: retry.xmrFailure, absent: absent})
	// This already runs in a tracked retry worker. Hand any queued replacement
	// through the normal admission continuation, including shutdown/policy waits.
	if next := s.runAdmittedXMRKeygen(s.xmrKeygen.finishAndPromote()); next != nil {
		s.retainXMRKeygen(next)
	}
}

// reportXMRKeygenFailure does not forward diagnostic blame, peer results, or
// empty failures. Singleton targets let honest reporters with different views
// converge without voting for another reporter's local absence list.
func (s *Signer) reportXMRKeygenFailure(block ttypes.KeygenBlock, keygen ttypes.Keygen, err error) {
	if keygen.Type != ttypes.KeygenType_XMRMonovaultKeygen || s.isStopped() {
		return
	}
	var failure *xmrKeygenFailure
	if !errors.As(err, &failure) || len(failure.absent) == 0 {
		return
	}
	validity, policyErr := s.keygenValidityBlocks(block)
	if policyErr != nil {
		return
	}
	height, heightErr := s.keygenCurrentHeight(block.Height)
	if heightErr != nil || !keygenHeightInWindow(height, block.Height, validity) {
		return
	}
	for _, absent := range failure.absent {
		if s.isStopped() {
			return
		}
		report := []ttypes.Blame{{
			FailReason: "absent throughout frost keygen admission",
			Round:      "frost_dkg_presence",
			BlameNodes: []ttypes.Node{{Pubkey: absent}},
		}}
		// Empty keys mean no backups or signer seed are needed.
		msg, buildErr := s.thorchainBridge.GetKeygenStdTx(
			common.EmptyPubKey, nil, nil, report, keygen.GetMembers(), keygen.Type,
			s.keygenChainsForType(keygen.Type), block.Height, 0, common.EmptyPubKey, nil,
			[]byte(nil), common.EmptyPubKey, int64(0),
		)
		if buildErr != nil {
			s.logger.Error().Err(buildErr).Msg("failed to build XMR keygen absence report")
			return
		}
		if s.isStopped() {
			return
		}
		if _, broadcastErr := s.broadcastKeygenMsgs(block.Height, msg); broadcastErr != nil {
			s.logger.Error().Err(broadcastErr).Msg("failed to broadcast XMR keygen absence report")
			return
		}
	}
}
