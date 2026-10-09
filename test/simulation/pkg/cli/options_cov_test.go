package cli

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func sendKeyCov(o *Options, key tcell.Key) {
	o.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), func(tview.Primitive) {})
}

func screenTextCov(t *testing.T, s tcell.SimulationScreen, row int) string {
	t.Helper()
	cells, width, _ := s.GetContents()
	var b strings.Builder
	for x := 0; x < width; x++ {
		b.WriteString(string(cells[row*width+x].Runes))
	}
	return strings.TrimRight(b.String(), " ")
}

func TestOptionsSelectedDefaultsCov(t *testing.T) {
	o := NewOptions(tview.NewApplication(), []Option{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
		{Name: "c", Default: true},
	})
	got := o.Selected()
	if len(got) != 2 || !got["a"] || !got["c"] || got["b"] {
		t.Fatalf("unexpected selection %v", got)
	}
	if o.currentOption != -1 {
		t.Fatalf("expected continue button focused initially, got %d", o.currentOption)
	}
}

func TestOptionsInputHandlerNavigationAndToggleCov(t *testing.T) {
	o := NewOptions(tview.NewApplication(), []Option{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
	})

	// up at the top stays on continue
	sendKeyCov(o, tcell.KeyUp)
	if o.currentOption != -1 {
		t.Fatalf("expected -1, got %d", o.currentOption)
	}

	// down moves through options and clamps at the last one
	sendKeyCov(o, tcell.KeyDown)
	sendKeyCov(o, tcell.KeyCtrlJ)
	sendKeyCov(o, tcell.KeyDown)
	if o.currentOption != 1 {
		t.Fatalf("expected clamp at 1, got %d", o.currentOption)
	}

	// enter toggles "b" on
	sendKeyCov(o, tcell.KeyEnter)
	if !o.Selected()["b"] {
		t.Fatal("expected b selected after toggle")
	}

	// ctrl-k back to "a" and toggle it off
	sendKeyCov(o, tcell.KeyCtrlK)
	sendKeyCov(o, tcell.KeyEnter)
	sel := o.Selected()
	if sel["a"] || !sel["b"] {
		t.Fatalf("unexpected selection %v", sel)
	}

	// enter on continue stops the (not running) app; current behaviour also toggles
	// the out-of-range -1 key, which does not affect the named selection
	sendKeyCov(o, tcell.KeyUp)
	sendKeyCov(o, tcell.KeyEnter)
	if o.currentOption != -1 {
		t.Fatalf("expected -1, got %d", o.currentOption)
	}
	if !o.unselected[-1] {
		t.Fatal("expected continue enter to flip the -1 entry (documents current behaviour)")
	}
	sel = o.Selected()
	if len(sel) != 1 || !sel["b"] {
		t.Fatalf("selection should be unchanged by continue, got %v", sel)
	}
}

func TestOptionsDrawCov(t *testing.T) {
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(40, 5)

	o := NewOptions(tview.NewApplication(), []Option{
		{Name: "alpha", Default: true},
		{Name: "beta", Default: false},
	})
	o.SetRect(0, 0, 40, 5)

	o.Draw(s)
	s.Show()
	if got := screenTextCov(t, s, 0); got != " Continue" {
		t.Fatalf("row0 = %q", got)
	}
	if got := screenTextCov(t, s, 1); got != " ◉ alpha" {
		t.Fatalf("row1 = %q", got)
	}
	if got := screenTextCov(t, s, 2); got != " ◯ beta" {
		t.Fatalf("row2 = %q", got)
	}
	_, _, style, _ := s.GetContent(0, 0)
	if _, bg, _ := style.Decompose(); bg != tcell.ColorGreen {
		t.Fatalf("continue should be green when focused, got %v", bg)
	}

	// focus the second option: continue becomes gray, option drawn green
	sendKeyCov(o, tcell.KeyDown)
	sendKeyCov(o, tcell.KeyDown)
	o.Draw(s)
	s.Show()
	_, _, style, _ = s.GetContent(0, 0)
	if _, bg, _ := style.Decompose(); bg != tcell.ColorGray {
		t.Fatalf("continue should be gray when unfocused, got %v", bg)
	}
	_, _, style, _ = s.GetContent(3, 2)
	if fg, _, _ := style.Decompose(); fg != tcell.ColorGreen {
		t.Fatalf("focused option should be green, got %v", fg)
	}
}
