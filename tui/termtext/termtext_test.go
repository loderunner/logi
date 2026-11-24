package termtext

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitize(t *testing.T) {
	t.Run("keeps plain text", func(t *testing.T) {
		require.Equal(t, "héllo, 世界", Sanitize("héllo, 世界"))
	})

	t.Run("replaces escape sequences", func(t *testing.T) {
		require.Equal(
			t,
			"�[31mred�[0m�",
			Sanitize("\x1b[31mred\x1b[0m\x07"),
		)
	})

	t.Run("replaces C1 controls and invalid UTF-8", func(t *testing.T) {
		require.Equal(t, "a�b�c", Sanitize("a\u009bb\xffc"))
	})

	t.Run("replaces tabs with spaces", func(t *testing.T) {
		require.Equal(t, "a    b", Sanitize("a\tb"))
	})
}
