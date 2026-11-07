package common

// PaymentsCoverTargets reports whether payments must have paid every one of
// the target actions. fits[p] lists the actions in [0, actions) that payment p
// could pay. Each payment pays one action and payments are assigned to as many
// distinct actions as they can be, so a target is certainly paid only when
// every such maximum assignment pays it: leaving it out must leave fewer
// payments assigned. A surplus payment, such as a second payment of a sibling,
// cannot be assigned anywhere and so proves nothing about a target.
//
// Targets are look-alike actions that an unpaid queued item could be: when any
// of them can be unpaid, the item may be that one. Only payments and actions
// connected to a target are weighed, so an unrelated over-payment elsewhere
// cannot make the targets look paid. With no payment that could pay a target,
// nothing is covered.
func PaymentsCoverTargets(fits [][]int, actions int, targets []int) bool {
	payers := make([][]int, actions)
	for p, fit := range fits {
		for _, a := range fit {
			payers[a] = append(payers[a], p)
		}
	}
	// Collect the payments and actions connected to a target.
	reachedAction := make([]bool, actions)
	reachedPayment := make([]bool, len(fits))
	queue := make([]int, 0, len(targets))
	for _, t := range targets {
		if !reachedAction[t] {
			reachedAction[t] = true
			queue = append(queue, t)
		}
	}
	var group []int
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		for _, p := range payers[a] {
			if reachedPayment[p] {
				continue
			}
			reachedPayment[p] = true
			group = append(group, p)
			for _, next := range fits[p] {
				if !reachedAction[next] {
					reachedAction[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	if len(group) == 0 {
		return false
	}
	most := assignedPayments(fits, actions, group, -1)
	for _, t := range targets {
		if assignedPayments(fits, actions, group, t) == most {
			return false
		}
	}
	return true
}

// assignedPayments returns how many payments in group can be given distinct
// actions they fit, other than skip (-1 skips none): a maximum matching, found
// by augmenting paths in group order.
func assignedPayments(fits [][]int, actions int, group []int, skip int) int {
	owner := make([]int, actions)
	for i := range owner {
		owner[i] = -1
	}
	var assign func(p int, seen []bool) bool
	assign = func(p int, seen []bool) bool {
		for _, a := range fits[p] {
			if a == skip || seen[a] {
				continue
			}
			seen[a] = true
			if owner[a] < 0 || assign(owner[a], seen) {
				owner[a] = p
				return true
			}
		}
		return false
	}
	assigned := 0
	for _, p := range group {
		if assign(p, make([]bool, actions)) {
			assigned++
		}
	}
	return assigned
}
