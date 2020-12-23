package types

import (
	fmt "fmt"
	"strconv"
	"strings"
)

func (n *Node) String() string {
	return fmt.Sprintf("%+v", n)
}

// IsEmpty check whether it is empty
func (b *Blame) IsEmpty() bool {
	return len(b.FailReason) == 0
}

// String implement fmt.Stringer
func (b Blame) String() string {
	sb := strings.Builder{}
	sb.WriteString("reason:" + b.FailReason + " is_unicast:" + strconv.FormatBool(b.IsUnicast) + "\n")
	sb.WriteString(fmt.Sprintf("nodes:%+v\n", b.BlameNodes))
	return sb.String()
}
