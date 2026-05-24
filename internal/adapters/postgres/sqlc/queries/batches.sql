-- batches.sql — query catalog for the batches table.

-- name: CreateBatch :exec
INSERT INTO batches (id, correlation_id, created_at)
VALUES ($1, $2, $3);

-- name: GetBatch :one
SELECT *
FROM batches
WHERE id = $1;
