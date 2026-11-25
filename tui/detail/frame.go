package detail

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The frame is a rounded border with the title in its top edge, and a blank
// column of padding inside each side, which shows the pane as a layer above
// the table. Below minFramedWidth columns or minFramedHeight lines, the pane
// drops the frame, so the little room there is goes to the text.
const (
	minFramedWidth  = 10
	minFramedHeight = 4
	padding         = 1
)

var (
	border      = lipgloss.RoundedBorder()
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(ansi.Cyan))
	titleStyle  = lipgloss.NewStyle().Bold(true)
)

// framed reports whether a pane of width columns and height lines has room for
// the frame.
func framed(width, height int) bool {
	return width >= minFramedWidth && height >= minFramedHeight
}

// textSize returns the columns and lines left for the text in a pane of width
// columns and height lines.
func textSize(width, height int) (int, int) {
	if !framed(width, height) {
		return width, height
	}

	return width - 2 - 2*padding, height - 2
}

// frame draws the frame of a pane of width columns and height lines around
// body, which holds the lines of text that textSize leaves room for. A title
// too long for the top edge ends with "…".
func frame(title, body string, width, height int) string {
	textWidth, _ := textSize(width, height)
	pad := strings.Repeat(" ", padding)
	side := borderStyle.Render(border.Left)

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		fill := strings.Repeat(" ", textWidth-ansi.StringWidth(line))
		lines[i] = side + pad + line + fill + pad +
			borderStyle.Render(border.Right)
	}

	return topEdge(title, width) + "\n" +
		strings.Join(lines, "\n") + "\n" +
		borderStyle.Render(
			border.BottomLeft+
				strings.Repeat(border.Bottom, width-2)+
				border.BottomRight,
		)
}

// topEdge returns the top edge of the frame, width columns wide, with the
// title set in it between a space on each side, at least one edge cell from
// each corner.
func topEdge(title string, width int) string {
	// The corners and an edge cell next to each.
	room := width - 4

	var label string
	if title != "" {
		label = ansi.Truncate(" "+title+" ", room, "… ")
	}

	rest := strings.Repeat(border.Top, room-ansi.StringWidth(label))

	return borderStyle.Render(border.TopLeft+border.Top) +
		titleStyle.Render(label) +
		borderStyle.Render(rest+border.Top+border.TopRight)
}
