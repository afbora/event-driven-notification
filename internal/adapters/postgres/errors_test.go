package postgres

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/afbora/event-driven-notification/internal/domain"
	"github.com/afbora/event-driven-notification/internal/ports"
)

// TestParseNotificationIDErr documents the standard wrap format used wherever
// a NotificationID string fails to parse as a UUID. Pinning the format also
// guards against a future refactor accidentally dropping the id from the
// message — operators rely on the id appearing in logs for correlation.
func TestParseNotificationIDErr(t *testing.T) {
	inner := errors.New("invalid UUID format")

	err := parseNotificationIDErr(domain.NotificationID("not-a-uuid"), inner)

	require.Error(t, err)
	require.Contains(t, err.Error(), `parse notification id "not-a-uuid"`)
	require.ErrorIs(t, err, inner, "wrapped error must remain reachable via errors.Is")
}

// TestWrapNotificationErr_ErrNotFound documents that the helper preserves
// the sentinel for errors.Is routing in the use case layer.
func TestWrapNotificationErr_ErrNotFound(t *testing.T) {
	err := wrapNotificationErr(ports.ErrNotFound, domain.NotificationID("01HXYZTEST00000000000000000"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "notification 01HXYZTEST00000000000000000")
	require.ErrorIs(t, err, ports.ErrNotFound)
}

// TestWrapNotificationErr_ErrAlreadyClaimed exercises the other concrete
// sentinel callers pass: the claim race signal that lets workers exit
// cleanly when another worker won the row.
func TestWrapNotificationErr_ErrAlreadyClaimed(t *testing.T) {
	err := wrapNotificationErr(ports.ErrAlreadyClaimed, domain.NotificationID("01HXYZCLAIM0000000000000000"))

	require.Error(t, err)
	require.ErrorIs(t, err, ports.ErrAlreadyClaimed)
}

// TestParseTemplateIDErr mirrors TestParseNotificationIDErr for the template
// CRUD helper; same shape, separate function because the id type differs.
func TestParseTemplateIDErr(t *testing.T) {
	inner := errors.New("invalid UUID format")

	err := parseTemplateIDErr(domain.TemplateID("not-a-uuid"), inner)

	require.Error(t, err)
	require.Contains(t, err.Error(), `parse template id "not-a-uuid"`)
	require.ErrorIs(t, err, inner)
}
