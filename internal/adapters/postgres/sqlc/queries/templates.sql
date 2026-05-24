-- templates.sql — query catalog for the templates table.

-- name: CreateTemplate :exec
INSERT INTO templates (id, name, channel, body, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetTemplate :one
SELECT *
FROM templates
WHERE id = $1;

-- name: GetTemplateByName :one
SELECT *
FROM templates
WHERE name = $1;

-- name: ListTemplates :many
SELECT *
FROM templates
ORDER BY name
LIMIT $1;

-- name: UpdateTemplate :exec
UPDATE templates
SET name = $2,
    channel = $3,
    body = $4
WHERE id = $1;

-- name: DeleteTemplate :exec
DELETE FROM templates
WHERE id = $1;
