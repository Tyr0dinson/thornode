package thorchain

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"

	"github.com/jinzhu/copier"
)

type orderItem struct {
	index int
	msg   MsgSwap
	fee   cosmos.Uint
	slip  cosmos.Uint
}

type orderItems []orderItem

func (items orderItems) HasItem(hash common.TxID) bool {
	for _, item := range items {
		if item.msg.Tx.ID.Equals(hash) {
			return true
		}
	}
	return false
}

type tradePair struct {
	source common.Asset
	target common.Asset
}

type tradePairs []tradePair

func genTradePair(s, t common.Asset) tradePair {
	return tradePair{
		source: s,
		target: t,
	}
}

func (pair tradePair) String() string {
	return fmt.Sprintf("%s>%s", pair.source, pair.target)
}

func (pair tradePair) HasRune() bool {
	return pair.source.IsNativeRune() || pair.target.IsNativeRune()
}

func (pair tradePair) Equals(p tradePair) bool {
	return pair.source.Equals(p.source) && pair.target.Equals(p.target)
}

// given a trade pair, find the trading pairs that are the reverse of this
// trade pair. This helps us build a list of trading pairs/order books to check
// for limit orders later
func (p tradePairs) findMatchingTrades(trade tradePair, pairs tradePairs) tradePairs {
	var comp func(pair tradePair) bool
	switch {
	case trade.source.IsNativeRune():
		comp = func(pair tradePair) bool { return pair.source.Equals(trade.target) }
	case trade.target.IsNativeRune():
		comp = func(pair tradePair) bool { return pair.target.Equals(trade.source) }
	default:
		comp = func(pair tradePair) bool { return pair.source.Equals(trade.target) || pair.target.Equals(trade.source) }
	}
	for _, pair := range pairs {
		if comp(pair) {
			// check for duplicates
			exists := false
			for _, p2 := range p {
				if p2.Equals(pair) {
					exists = true
					break
				}
			}
			if !exists {
				p = append(p, pair)
			}
		}
	}
	return p
}

func (items orderItems) Sort(ctx cosmos.Context) orderItems {
	// sort by liquidity fee , descending

	byFee := make(orderItems, len(items))
	if err := copier.Copy(&byFee, &items); err != nil {
		ctx.Logger().Error("fail copy items by fee", "error", err)
	}
	sort.SliceStable(byFee, func(i, j int) bool {
		return byFee[i].fee.GT(byFee[j].fee)
	})

	// sort by slip fee , descending
	bySlip := make(orderItems, len(items))
	if err := copier.Copy(&bySlip, &items); err != nil {
		ctx.Logger().Error("fail copy items by slip", "error", err)
	}
	sort.SliceStable(bySlip, func(i, j int) bool {
		return bySlip[i].slip.GT(bySlip[j].slip)
	})

	type score struct {
		msg   MsgSwap
		score int
		index int
	}

	// add liquidity fee score
	scores := make([]score, len(items))
	for i, item := range byFee {
		scores[i] = score{
			msg:   item.msg,
			score: i,
			index: item.index,
		}
	}

	// add slip score
	for i, item := range bySlip {
		for j, score := range scores {
			if score.msg.Tx.ID.Equals(item.msg.Tx.ID) && score.index == item.index {
				scores[j].score += i
				break
			}
		}
	}

	// sorting by two attribute to ensure there is no abiguity here, since
	// multiple items can have the same score (but not the same tx hash)
	sort.SliceStable(scores, func(i, j int) bool {
		switch {
		case scores[i].score < scores[j].score:
			return true
		case scores[i].score == scores[j].score:
			return scores[i].msg.Tx.ID.String() < scores[j].msg.Tx.ID.String()
		default:
			return false
		}
	})

	// sort our items by score
	sorted := make(orderItems, len(items))
	for i, score := range scores {
		for _, item := range items {
			if item.msg.Tx.ID.Equals(score.msg.Tx.ID) && score.index == item.index {
				sorted[i] = item
				break
			}
		}
	}

	return sorted
}
