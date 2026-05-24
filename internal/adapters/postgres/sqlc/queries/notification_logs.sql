-- notification_logs.sql — query catalog for the notification_logs table.

-- name: AppendNotificationLog :exec
INSERT INTO notification_logs (
    id, notification_id, correlation_id, event, details, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6
);

-- name: ListNotificationLogs :many
SELECT *
FROM notification_logs
WHERE notification_id = $1
ORDER BY created_at;
