package row

import (
	"strings"

	grid "github.com/achannarasappa/term-grid"
	"github.com/charmbracelet/x/ansi"
)

// term-grid clips bytes, which corrupts styled or wide-character cells.
func renderCells(cells []grid.Cell, width int) string {
	visible := make([]grid.Cell, 0, len(cells))
	remaining, flex := width, 0
	for _, cell := range cells {
		if cell.Width < 0 || cell.VisibleMinWidth > width {
			continue
		}
		visible = append(visible, cell)
		remaining -= cell.Width
		if cell.Width == 0 {
			flex++
		}
	}
	remaining = max(0, remaining-WidthGutter*max(0, len(visible)-1))
	for i := range visible {
		if visible[i].Width == 0 && flex > 0 {
			visible[i].Width = (remaining + flex - 1) / flex
			remaining -= visible[i].Width
			flex--
		}
	}
	lines := []string{}
	used := 0
	for i, cell := range visible {
		cellLines := strings.Split(cell.Text, "\n")
		for len(lines) < len(cellLines) {
			lines = append(lines, strings.Repeat(" ", used))
		}
		gutter := ""
		if i < len(visible)-1 {
			gutter = strings.Repeat(" ", WidthGutter)
		}
		for j := range lines {
			text := ""
			if j < len(cellLines) {
				text = ansi.Truncate(cellLines[j], cell.Width, "")
			}
			padding := strings.Repeat(" ", max(0, cell.Width-ansi.StringWidth(text)))
			if cell.Align == grid.Right {
				text = padding + text
			} else {
				text += padding
			}
			lines[j] += text + gutter
		}
		used += cell.Width + len(gutter)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	return strings.Join(lines, "\n")
}
