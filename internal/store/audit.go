package store

import "context"

func addAudit(ctx context.Context, tx sqlExecutor, actorID int64, action, targetType, targetID, metadata string) error {
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events (actor_user_id, action, target_type, target_id, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		actor, action, targetType, targetID, metadata, nowUnix())
	return err
}
