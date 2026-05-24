-- notifications.sql — query catalog for the notifications table.
-- Additional queries (ClaimForProcessing, List with cursor, reconciler
-- scans) land in this file in later phase-3 tasks (PLAN.md tasks 7, 11, 13).

-- name: CreateNotification :exec
INSERT INTO notifications (
    id, batch_id, idempotency_key, correlation_id,
    channel, priority, recipient, content,
    status, attempts, last_error,
    next_retry_at, scheduled_at, template_id,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11,
    $12, $13, $14,
    $15, $16
);

-- name: GetNotification :one
SELECT *
FROM notifications
WHERE id = $1;

-- ClaimForProcessing atomically moves a notification from queued/retrying
-- into processing and increments the attempts counter. Returning zero rows
-- means another worker (or a redelivery) won the race; the repository
-- surfaces this as ports.ErrAlreadyClaimed. See CLAUDE.md §3.10 / ADR-0009.

-- name: ClaimForProcessing :one
UPDATE notifications
SET    status     = 'processing',
       updated_at = $2,
       attempts   = attempts + 1
WHERE  id = $1
  AND  status IN ('queued', 'retrying')
RETURNING *;
