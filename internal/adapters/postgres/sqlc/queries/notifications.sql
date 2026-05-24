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

-- UpdateNotificationStatus persists the mutations a use case applied to
-- a notification entity (Cancel / MarkDelivered / MarkFailed / MarkRetrying).
-- The WHERE clause includes the expected source status as a concurrency
-- guard: zero rows affected means another writer changed the row between
-- the read and this update. The repository surfaces this as
-- ports.ErrConcurrentUpdate.

-- name: UpdateNotificationStatus :execrows
UPDATE notifications
SET    status        = sqlc.arg('new_status'),
       attempts      = sqlc.arg('attempts'),
       last_error    = sqlc.arg('last_error'),
       next_retry_at = sqlc.arg('next_retry_at'),
       updated_at    = sqlc.arg('updated_at')
WHERE  id              = sqlc.arg('id')
  AND  status          = sqlc.arg('expected_source');

-- ListNotifications returns a page of notifications matching the supplied
-- filters, ordered by (created_at DESC, id DESC). The keyset cursor (the
-- last row's (created_at, id) tuple, base64-encoded by the repository) is
-- compared as a Postgres composite so same-microsecond timestamps tiebreak
-- on the id column. Every filter is nullable via sqlc.narg — pass NULL
-- to skip the predicate. The repository fetches limit+1 rows so it can
-- tell when more pages exist without an extra count query.

-- name: ListNotifications :many
SELECT *
FROM   notifications
WHERE  (sqlc.narg('status')::text          IS NULL OR status   = sqlc.narg('status'))
  AND  (sqlc.narg('channel')::text         IS NULL OR channel  = sqlc.narg('channel'))
  AND  (sqlc.narg('batch_id')::uuid        IS NULL OR batch_id = sqlc.narg('batch_id'))
  AND  (sqlc.narg('created_after')::timestamptz  IS NULL OR created_at >= sqlc.narg('created_after'))
  AND  (sqlc.narg('created_before')::timestamptz IS NULL OR created_at <= sqlc.narg('created_before'))
  AND  (sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');

-- Reconciler queries (CLAUDE.md §3.11, ADR-0011). Each uses FOR UPDATE
-- SKIP LOCKED so multiple reconciler instances can scan in parallel
-- without conflicting claims — a row another reconciler has already
-- locked is invisible to this query rather than blocking it.

-- name: FindOrphanedPending :many
SELECT *
FROM   notifications
WHERE  status      = 'pending'
  AND  created_at  < sqlc.arg('older_than')
ORDER BY created_at
LIMIT sqlc.arg('row_limit')
FOR UPDATE SKIP LOCKED;

-- name: FindStuckProcessing :many
SELECT *
FROM   notifications
WHERE  status      = 'processing'
  AND  updated_at  < sqlc.arg('older_than')
ORDER BY updated_at
LIMIT sqlc.arg('row_limit')
FOR UPDATE SKIP LOCKED;

-- name: FindOverdueRetrying :many
SELECT *
FROM   notifications
WHERE  status         = 'retrying'
  AND  next_retry_at IS NOT NULL
  AND  next_retry_at  < sqlc.arg('before_at')
ORDER BY next_retry_at
LIMIT sqlc.arg('row_limit')
FOR UPDATE SKIP LOCKED;
