package ui

import "testing"

func TestComputeTableColsFitsWidth(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 160, 200} {
		c := computeTableCols(w)
		row := c.formatRow(
			"app", "argocd", "guestbook",
			"default", "OutOfSync", "Healthy", "abcdef1", "3d",
		)
		row += " ⏸ !"
		if got := len([]rune(row)); got > w+2 { // small slack for emoji width vs runes
			t.Fatalf("width %d: row runes=%d cols=%+v\n%s", w, got, c, row)
		}
		if c.name < 8 {
			t.Fatalf("width %d: name too small: %d", w, c.name)
		}
	}
}

func TestComputeTableColsDropsOnNarrow(t *testing.T) {
	c := computeTableCols(70)
	if c.showRev {
		t.Fatal("expected REV hidden on narrow terminal")
	}
	wide := computeTableCols(130)
	if !wide.showNS || !wide.showProject || !wide.showRev {
		t.Fatalf("expected all columns on wide terminal: %+v", wide)
	}
}
