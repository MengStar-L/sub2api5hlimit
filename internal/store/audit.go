package store

import "context"

// RecordAudit appends an audit event without exposing the audit table through
// the public HTTP API. Callers must keep metadata free of credentials.
func (s *Store) RecordAudit(ctx context.Context, actorID int64, action, targetType, targetID, metadata string) error {
	return addAudit(ctx, s.db, actorID, action, targetType, targetID, metadata)
}

func addAudit(ctx context.Context, tx sqlExecutor, actorID int64, action, targetType, targetID, metadata string) error {
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events (actor_user_id, action, target_type, target_id, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		actor, action, targetType, targetID, metadata, nowUnix())
	return err
}
