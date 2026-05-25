package http

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWrapMapTemplate documents the standard wrap format used by every
// template handler when toAPITemplate fails (typically because a stored
// template id is not a valid UUID — a defensive guard, since Create
// rejects invalid ids on the way in).
func TestWrapMapTemplate(t *testing.T) {
	inner := errors.New("template id is not a uuid")

	err := wrapMapTemplate(inner)

	require.Error(t, err)
	require.Contains(t, err.Error(), "map template")
	require.ErrorIs(t, err, inner, "wrapped error must remain reachable via errors.Is")
}
