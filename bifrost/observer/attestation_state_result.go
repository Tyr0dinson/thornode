package observer

// A completed legacy exchange does not prove that asynchronous imports
// accepted evidence. The compatibility receiver retains that completion policy.
type attestationStateGroupResult struct {
	completed, failed int
}

func (r *attestationStateGroupResult) add(other attestationStateGroupResult) {
	r.completed += other.completed
	r.failed += other.failed
}

func (r attestationStateGroupResult) completionReason() string {
	if r.completed > 0 {
		return "completed_exchange"
	}
	return ""
}

func (s *AttestationGossip) logAttestationStateCollection(c *attestationStateCollection, reason string) {
	event := s.logger.Info()
	if c.result.completed == 0 {
		event = s.logger.Warn()
	}
	event.Str("reason", reason).
		Int("requests", c.requests).Int("request_limit", c.requestLimit).
		Int("completed", c.result.completed).Int("failed", c.result.failed).
		Msg("startup attestation state retry window ended")
}
