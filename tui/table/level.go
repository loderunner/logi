package table

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Styles of the known log levels. A level that isn't known keeps the
// terminal's foreground.
var (
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(ansi.Red))
	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.Yellow))
	infoStyle  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(ansi.Blue))
	debugStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(ansi.BrightBlack))
)

// levelStyle returns the style of a log level, without regard to case, and
// whether the level is known. The spellings are the common ones from loggers
// that write a word, such as slog, zap and logrus. Loggers that write a number
// aren't covered.
func levelStyle(level string) (lipgloss.Style, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "err":
		return errorStyle, true
	case "warning", "warn":
		return warningStyle, true
	case "info":
		return infoStyle, true
	case "debug":
		return debugStyle, true
	default:
		return lipgloss.Style{}, false
	}
}
