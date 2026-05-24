//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pageTimeout is the request budget for each list call. A few seconds
// is plenty — keyset pagination on a tiny dataset is fast.
const pageTimeout = 5 * time.Second

// TestPagination_ForwardCursorCoversEveryRow: create N notifications,
// walk the list endpoint with a small page size, and verify the
// cursor chain returns every id exactly once. This is the
// load-bearing claim that keyset pagination
// (postgres adapter) survives the round-trip via the HTTP layer.
func TestPagination_ForwardCursorCoversEveryRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	const total = 7
	const pageSize = 3

	// Create sequentially so created_at is monotonic — keyset
	// pagination relies on (created_at, id) ordering and ambiguous
	// timestamps would make the test flaky.
	posted := make([]string, total)
	for i := 0; i < total; i++ {
		posted[i] = createNotification(ctx, t, h.BaseURL, fmt.Sprintf("+1555555506%d", i))
	}

	seen := make(map[string]bool, total)
	pages := 0
	cursor := ""
	for {
		pages++
		page := fetchPage(ctx, t, h.BaseURL, cursor, pageSize)
		require.LessOrEqualf(t, len(page.Items), pageSize,
			"page %d returned more than limit", pages)

		for _, item := range page.Items {
			require.Falsef(t, seen[item.ID],
				"duplicate id across pages: %s on page %d", item.ID, pages)
			seen[item.ID] = true
		}

		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor

		require.LessOrEqualf(t, pages, total,
			"pagination did not terminate; cursor=%q", cursor)
	}

	// Every posted id must appear in the walk; no extra rows because
	// the harness's database is fresh.
	for _, id := range posted {
		require.Truef(t, seen[id], "id %s missing from cursor walk", id)
	}
	require.Equal(t, total, len(seen), "cursor walk returned an unexpected number of rows")

	// Expected page count: ceil(7/3) = 3.
	require.Equal(t, 3, pages, "with N=7 and limit=3 the walk should be 3 pages, got %d", pages)
}

// TestPagination_EmptyResultStillTerminates: an empty list (no rows
// match the filter) returns one page with no items and no cursor.
// Guards against an off-by-one that loops forever on an empty
// dataset.
func TestPagination_EmptyResultStillTerminates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h := NewHarness(ctx, t)

	page := fetchPage(ctx, t, h.BaseURL, "", 50)
	require.Empty(t, page.Items, "fresh harness should return zero rows")
	require.Empty(t, page.NextCursor, "no rows → no next cursor")
}

// pageResult mirrors the on-wire NotificationPage shape used by tests
// to walk the cursor chain. Keeping it private avoids dragging in the
// generated api package for a one-off shape.
type pageResult struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func fetchPage(ctx context.Context, t *testing.T, baseURL, cursor string, limit int) pageResult {
	t.Helper()
	reqCtx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/api/v1/notifications?limit=%d", baseURL, limit)
	if cursor != "" {
		url += "&cursor=" + cursor
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list body=%s", body)
	var out pageResult
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}
