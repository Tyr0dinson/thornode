package common

import (
	. "gopkg.in/check.v1"
)

type OutboundCoverSuite struct{}

var _ = Suite(&OutboundCoverSuite{})

func (s *OutboundCoverSuite) TestPaymentsCoverTargets(c *C) {
	// A unique action and its payment.
	c.Check(PaymentsCoverTargets([][]int{{0}}, 1, []int{0}), Equals, true)
	c.Check(PaymentsCoverTargets(nil, 1, []int{0}), Equals, false)

	// Equal twins: one payment leaves one unpaid, two pay both.
	c.Check(PaymentsCoverTargets([][]int{{0, 1}}, 2, []int{0, 1}), Equals, false)
	c.Check(PaymentsCoverTargets([][]int{{0, 1}, {0, 1}}, 2, []int{0, 1}), Equals, true)

	// Unequal legs: a payment fitting both, and the sibling's own payment.
	c.Check(PaymentsCoverTargets([][]int{{0, 1}}, 2, []int{0}), Equals, false)
	c.Check(PaymentsCoverTargets([][]int{{1}}, 2, []int{0}), Equals, false)
	c.Check(PaymentsCoverTargets([][]int{{0, 1}, {1}}, 2, []int{0}), Equals, true)
	c.Check(PaymentsCoverTargets([][]int{{1}, {0, 1}}, 2, []int{0}), Equals, true)

	// A paid pair stays paid when a sibling payment that also fits a partly
	// paid pair joins it.
	fits := [][]int{{0, 1}, {0, 1}, {0, 1, 2, 3}}
	c.Check(PaymentsCoverTargets(fits, 4, []int{0, 1}), Equals, true)
	// The partly paid pair is not.
	c.Check(PaymentsCoverTargets(fits, 4, []int{2, 3}), Equals, false)

	// An over-payment of an unrelated action does not cover the targets.
	c.Check(PaymentsCoverTargets([][]int{{1}, {1}}, 2, []int{0}), Equals, false)

	// A sibling paid twice, connected to unpaid targets by one payment that
	// could pay any of them: that one payment covers at most one target.
	overpaid := [][]int{{0}, {0}, {0, 1, 2, 3}}
	c.Check(PaymentsCoverTargets(overpaid, 4, []int{1, 2, 3}), Equals, false)
	c.Check(PaymentsCoverTargets([][]int{{0}, {0}, {0, 1, 2}}, 3, []int{1, 2}), Equals, false)
	// With a single target, the only payment that can be assigned beyond the
	// sibling's is the target's.
	c.Check(PaymentsCoverTargets([][]int{{0}, {0}, {0, 1}}, 2, []int{1}), Equals, true)
}
