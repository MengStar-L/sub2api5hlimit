package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
)

func TestSetupTokenAndAdminKeyStorage(t *testing.T) {
	ctx := context.Background()
	store, databasePath := openTestStore(t)
	plainSetupToken := "setup-token-plaintext-sentinel"
	setupTokenHash := secure.HashToken(plainSetupToken)
	if err := store.SetSetupToken(ctx, setupTokenHash, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatalf("SetSetupToken() error = %v", err)
	}

	var storedTokenHash sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT setup_token_hash FROM app_meta WHERE id=1`).Scan(&storedTokenHash); err != nil {
		t.Fatalf("read setup token hash: %v", err)
	}
	if !storedTokenHash.Valid || storedTokenHash.String != setupTokenHash {
		t.Fatalf("stored setup token = %#v, want its hash", storedTokenHash)
	}
	if storedTokenHash.String == plainSetupToken {
		t.Fatal("setup token was stored in plaintext")
	}

	adminKey := "admin-api-key-plaintext-sentinel"
	settings := Settings{
		ConnectionUUID:   "connection-1",
		BaseURL:          "https://sub2api.example.com",
		AdminAPIKey:      adminKey,
		OwnerUserID:      42,
		OwnerLabel:       "distribution-owner",
		AllowPrivateHTTP: false,
	}
	admin := User{Username: "admin", DisplayName: "Administrator"}
	if err := store.CompleteSetup(ctx, "wrong-token-hash", admin, "password-hash", settings); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("CompleteSetup(wrong token) error = %v, want ErrInvalidToken", err)
	}
	if err := store.CompleteSetup(ctx, setupTokenHash, admin, "password-hash", settings); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}

	status, err := store.SetupStatus(ctx)
	if err != nil {
		t.Fatalf("SetupStatus() error = %v", err)
	}
	if !status.Complete || status.ExpiresAt != 0 {
		t.Fatalf("SetupStatus() = %#v", status)
	}
	if err := store.SetSetupToken(ctx, setupTokenHash, time.Now().Add(time.Minute).Unix()); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("SetSetupToken(after setup) error = %v, want ErrSetupComplete", err)
	}

	loaded, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if loaded.AdminAPIKey != adminKey || loaded.ConnectionUUID != settings.ConnectionUUID {
		t.Fatalf("GetSettings() = %#v", loaded)
	}
	var cipher string
	if err := store.db.QueryRowContext(ctx, `SELECT admin_api_key_cipher FROM settings WHERE id=1`).Scan(&cipher); err != nil {
		t.Fatalf("read encrypted key: %v", err)
	}
	if cipher == adminKey || strings.Contains(cipher, adminKey) {
		t.Fatal("settings table contains the administrator key in plaintext")
	}
	var clearedHash, clearedExpiry any
	if err := store.db.QueryRowContext(ctx, `SELECT setup_token_hash, setup_token_expires_at FROM app_meta WHERE id=1`).Scan(&clearedHash, &clearedExpiry); err != nil {
		t.Fatalf("read cleared setup token: %v", err)
	}
	if clearedHash != nil || clearedExpiry != nil {
		t.Fatalf("setup token was not cleared: hash=%v expiry=%v", clearedHash, clearedExpiry)
	}

	assertFilesDoNotContain(t, databasePath, []string{plainSetupToken, adminKey})
}

func TestCompleteSetupRejectsExpiredToken(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	hash := secure.HashToken("expired-setup-token")
	if err := store.SetSetupToken(ctx, hash, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	err := store.CompleteSetup(ctx, hash, User{Username: "admin", DisplayName: "Admin"}, "hash", Settings{
		ConnectionUUID: "connection", BaseURL: "https://example.com", AdminAPIKey: "admin-key", OwnerUserID: 1,
	})
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("CompleteSetup(expired token) error = %v, want ErrInvalidToken", err)
	}
}

func TestKeyBindingOneToOneConstraints(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	sharedKey := compliantSnapshot(101)
	first, err := store.CreateUser(ctx, "first", "First", "hash", &sharedKey, 0)
	if err != nil {
		t.Fatalf("CreateUser(first) error = %v", err)
	}
	if first.Binding == nil || first.Binding.UpstreamKeyID != sharedKey.UpstreamKeyID {
		t.Fatalf("first binding = %#v", first.Binding)
	}

	if _, err := store.CreateUser(ctx, "rolled-back", "Rolled Back", "hash", &sharedKey, 0); !errors.Is(err, ErrKeyBound) {
		t.Fatalf("CreateUser(duplicate key) error = %v, want ErrKeyBound", err)
	}
	if _, err := store.UserByUsername(ctx, "rolled-back"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("duplicate-key user was not rolled back: %v", err)
	}

	second, err := store.CreateUser(ctx, "second", "Second", "hash", nil, 0)
	if err != nil {
		t.Fatalf("CreateUser(second) error = %v", err)
	}
	if err := store.SetBinding(ctx, second.ID, sharedKey, 0); !errors.Is(err, ErrKeyBound) {
		t.Fatalf("SetBinding(duplicate key) error = %v, want ErrKeyBound", err)
	}

	replacement := compliantSnapshot(202)
	if err := store.SetBinding(ctx, first.ID, replacement, 0); err != nil {
		t.Fatalf("SetBinding(replacement) error = %v", err)
	}
	binding, err := store.BindingByUser(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.UpstreamKeyID != replacement.UpstreamKeyID {
		t.Fatalf("replacement binding = %#v", binding)
	}
	if err := store.SetBinding(ctx, second.ID, sharedKey, 0); err != nil {
		t.Fatalf("released key could not be rebound: %v", err)
	}
	bindings, err := store.ListBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("bindings = %#v, want exactly one per user", bindings)
	}
}

func TestBindingsRequirePositiveFiveAndSevenDayLimits(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	tests := []struct {
		name  string
		five  float64
		seven float64
	}{
		{name: "zero five-hour", five: 0, seven: 100},
		{name: "negative five-hour", five: -1, seven: 100},
		{name: "zero seven-day", five: 10, seven: 0},
		{name: "negative seven-day", five: 10, seven: -1},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := compliantSnapshot(int64(index + 1))
			snapshot.RateLimit5h = test.five
			snapshot.RateLimit7d = test.seven
			if snapshot.Compliant() {
				t.Fatal("KeySnapshot.Compliant() accepted a non-positive window")
			}
			username := "invalid-" + strings.ReplaceAll(test.name, " ", "-")
			if _, err := store.CreateUser(ctx, username, test.name, "hash", &snapshot, 0); err == nil {
				t.Fatal("CreateUser() accepted a non-compliant key")
			}
			if _, err := store.UserByUsername(ctx, username); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed user creation was not atomic: %v", err)
			}
		})
	}

	user, err := store.CreateUser(ctx, "valid-user", "Valid", "hash", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	invalid := compliantSnapshot(999)
	invalid.RateLimit7d = 0
	if err := store.SetBinding(ctx, user.ID, invalid, 0); err == nil {
		t.Fatal("SetBinding() accepted a key without both positive limits")
	}
	valid := compliantSnapshot(1000)
	if err := store.SetBinding(ctx, user.ID, valid, 0); err != nil {
		t.Fatalf("SetBinding(valid) error = %v", err)
	}
}

func TestDisablingAndPasswordResetRevokeAllUserSessions(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	user, err := store.CreateUser(ctx, "session-user", "Session User", "old-hash", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateUser(ctx, "other-user", "Other User", "hash", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	createSessions(t, store, user.ID, "first", "second")
	createSessions(t, store, other.ID, "other")

	if err := store.UpdateUser(ctx, user.ID, user.DisplayName, StatusDisabled, 0); err != nil {
		t.Fatalf("UpdateUser(disable) error = %v", err)
	}
	assertSessionCount(t, store, user.ID, 0)
	assertSessionCount(t, store, other.ID, 1)
	if _, err := store.Session(ctx, "first", 110, 150); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled user's session error = %v, want ErrNotFound", err)
	}

	if err := store.UpdateUser(ctx, user.ID, user.DisplayName, StatusActive, 0); err != nil {
		t.Fatalf("UpdateUser(reactivate) error = %v", err)
	}
	createSessions(t, store, user.ID, "third", "fourth")
	if err := store.SetUserPassword(ctx, user.ID, "new-hash", 0); err != nil {
		t.Fatalf("SetUserPassword() error = %v", err)
	}
	assertSessionCount(t, store, user.ID, 0)
	assertSessionCount(t, store, other.ID, 1)
	updated, err := store.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PasswordHash != "new-hash" {
		t.Fatalf("password hash = %q", updated.PasswordHash)
	}
}

func TestDeleteUserIsSoftAndRemovesAccessState(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	snapshot := compliantSnapshot(303)
	user, err := store.CreateUser(ctx, "delete-user", "Delete User", "hash", &snapshot, 0)
	if err != nil {
		t.Fatal(err)
	}
	createSessions(t, store, user.ID, "delete-session")

	if err := store.DeleteUser(ctx, user.ID, 0); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	deleted, err := store.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("soft-deleted row is unavailable by ID: %v", err)
	}
	if deleted.Status != StatusDeleted || deleted.DeletedAt == nil {
		t.Fatalf("deleted user = %#v", deleted)
	}
	if deleted.Binding != nil {
		t.Fatalf("deleted user retained binding: %#v", deleted.Binding)
	}
	if _, err := store.UserByUsername(ctx, user.Username); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UserByUsername(deleted) error = %v, want ErrNotFound", err)
	}
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("ListUsers() includes deleted user: %#v", users)
	}
	assertSessionCount(t, store, user.ID, 0)
	if _, err := store.BindingByUser(ctx, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BindingByUser(deleted) error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteUser(ctx, user.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteUser() error = %v, want ErrNotFound", err)
	}
}

func TestApplyQuotaResetSnapshotUpdatesOnlySafeUsageFields(t *testing.T) {
	ctx := context.Background()
	data, _ := openTestStore(t)
	reset5h := int64(1700)
	reset7d := int64(2300)
	sourceBefore := int64(1600)
	snapshot := compliantSnapshot(350)
	snapshot.Name = "quota-key"
	snapshot.Mask = "sk-…safe"
	snapshot.Status = "active"
	snapshot.Usage5h = 8
	snapshot.Usage7d = 80
	snapshot.Reset5hAt = &reset5h
	snapshot.Reset7dAt = &reset7d
	snapshot.SourceUpdatedAt = &sourceBefore
	user, err := data.CreateUser(ctx, "quota-user", "Quota User", "hash", &snapshot, 0)
	if err != nil {
		t.Fatal(err)
	}

	sourceAfter := int64(1999)
	if err := data.ApplyQuotaResetSnapshot(ctx, snapshot.UpstreamKeyID, 0, 0, nil, nil, &sourceAfter, 2000); err != nil {
		t.Fatalf("ApplyQuotaResetSnapshot() error = %v", err)
	}
	binding, err := data.BindingByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Usage5h != 0 || binding.Usage7d != 0 || binding.Reset5hAt != nil || binding.Reset7dAt != nil {
		t.Fatalf("reset usage fields = %#v", binding)
	}
	if binding.SourceUpdatedAt == nil || *binding.SourceUpdatedAt != sourceAfter || binding.LastSuccessAt == nil || *binding.LastSuccessAt != 2000 {
		t.Fatalf("reset snapshot timestamps = %#v", binding)
	}
	if binding.UpstreamKeyID != snapshot.UpstreamKeyID || binding.KeyName != snapshot.Name || binding.KeyMask != snapshot.Mask ||
		binding.UpstreamStatus != snapshot.Status || binding.RateLimit5h != snapshot.RateLimit5h || binding.RateLimit7d != snapshot.RateLimit7d {
		t.Fatalf("reset changed immutable binding fields: %#v", binding)
	}
	if err := data.ApplyQuotaResetSnapshot(ctx, 999999, 0, 0, nil, nil, nil, 2001); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplyQuotaResetSnapshot(missing) error = %v, want ErrNotFound", err)
	}
}

func TestKeySnapshotMissingConfigurationErrorAndRecovery(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	snapshot := compliantSnapshot(404)
	user, err := store.CreateUser(ctx, "sync-user", "Sync User", "hash", &snapshot, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ApplyKeySnapshots(ctx, nil, 2000); err != nil {
		t.Fatalf("ApplyKeySnapshots(missing) error = %v", err)
	}
	binding, err := store.BindingByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.BindingState != "missing" || binding.LastErrorCode != "UPSTREAM_KEY_MISSING" {
		t.Fatalf("missing binding = %#v", binding)
	}

	recovered := compliantSnapshot(snapshot.UpstreamKeyID)
	recovered.Name = "recovered-key"
	recovered.Usage5h = 4.5
	recovered.Usage7d = 45
	sourceUpdatedAt := int64(1999)
	recovered.SourceUpdatedAt = &sourceUpdatedAt
	if err := store.ApplyKeySnapshots(ctx, []KeySnapshot{recovered}, 2100); err != nil {
		t.Fatalf("ApplyKeySnapshots(recovered) error = %v", err)
	}
	binding, err = store.BindingByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.BindingState != "healthy" || binding.LastErrorCode != "" || binding.KeyName != "recovered-key" {
		t.Fatalf("recovered binding = %#v", binding)
	}
	if binding.LastSuccessAt == nil || *binding.LastSuccessAt != 2100 || binding.SourceUpdatedAt == nil || *binding.SourceUpdatedAt != sourceUpdatedAt {
		t.Fatalf("recovered timestamps = %#v", binding)
	}

	misconfigured := recovered
	misconfigured.RateLimit5h = 0
	if err := store.ApplyKeySnapshots(ctx, []KeySnapshot{misconfigured}, 2200); err != nil {
		t.Fatalf("ApplyKeySnapshots(misconfigured) error = %v", err)
	}
	binding, err = store.BindingByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.BindingState != "invalid_limits" {
		t.Fatalf("misconfigured binding state = %q", binding.BindingState)
	}
}

func TestPoolUnsupportedWindowsAndPublicationConstraints(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	inventory := PoolInventory{
		UpstreamAccountID: 10,
		Name:              "pool@example.com",
		Email:             "pool@example.com",
		Platform:          "openai",
		AccountType:       "oauth",
		PlanType:          "pro",
		Status:            "active",
		Schedulable:       true,
	}
	if err := store.ApplyPoolInventory(ctx, []PoolInventory{inventory}, 1000); err != nil {
		t.Fatalf("ApplyPoolInventory() error = %v", err)
	}
	accounts, err := store.ListPool(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("pool = %#v", accounts)
	}
	alias := accounts[0].PublicAlias
	if alias == "" {
		t.Fatal("pool account has no persistent public alias")
	}

	if err := store.ApplyPoolUsage(ctx, []PoolUsage{{
		UpstreamAccountID: 10,
		FiveSupported:     false,
		FiveUtilization:   nil,
		FiveResetAt:       nil,
		SevenSupported:    false,
		SevenUtilization:  nil,
		SevenResetAt:      nil,
		Source:            "passive",
	}}, 1100); err != nil {
		t.Fatalf("ApplyPoolUsage() error = %v", err)
	}
	accounts, err = store.ListPool(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	account := accounts[0]
	if account.FiveSupported || account.SevenSupported || account.FiveUtilization != nil || account.SevenUtilization != nil {
		t.Fatalf("unsupported windows were converted to zero values: %#v", account)
	}
	if account.FiveResetAt != nil || account.SevenResetAt != nil {
		t.Fatalf("unsupported reset times were populated: %#v", account)
	}

	if err := store.SetPoolPublished(ctx, []int64{10, 999}, true, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPoolPublished(partial invalid batch) error = %v, want ErrNotFound", err)
	}
	accounts, err = store.ListPool(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[0].Published {
		t.Fatal("failed publication batch was not rolled back")
	}
	if err := store.SetPoolPublished(ctx, []int64{10}, true, 0); err != nil {
		t.Fatalf("SetPoolPublished() error = %v", err)
	}
	ids, err := store.PublishedAccountIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int64{10}) {
		t.Fatalf("PublishedAccountIDs() = %v", ids)
	}

	if err := store.ApplyPoolInventory(ctx, nil, 1200); err != nil {
		t.Fatalf("ApplyPoolInventory(missing) error = %v", err)
	}
	ids, err = store.PublishedAccountIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("missing published account remained syncable: %v", ids)
	}
	if err := store.SetPoolPublished(ctx, []int64{10}, true, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPoolPublished(missing) error = %v, want ErrNotFound", err)
	}

	if err := store.ApplyPoolInventory(ctx, []PoolInventory{inventory}, 1300); err != nil {
		t.Fatalf("ApplyPoolInventory(recovered) error = %v", err)
	}
	accounts, err = store.ListPool(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[0].Missing || accounts[0].PublicAlias != alias {
		t.Fatalf("recovered pool account = %#v, want stable alias %q", accounts[0], alias)
	}
}

func TestPoolUsageFailurePreservesLastSuccessfulSnapshot(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	if err := store.ApplyPoolInventory(ctx, []PoolInventory{{
		UpstreamAccountID: 11, Name: "pool", Email: "pool@example.com", Platform: "openai",
		AccountType: "oauth", Status: "active", Schedulable: true,
	}}, 1000); err != nil {
		t.Fatal(err)
	}
	five, seven := 18.5, 46.25
	fiveReset, sevenReset, sourceAt := int64(2000), int64(3000), int64(1500)
	if err := store.ApplyPoolUsage(ctx, []PoolUsage{{
		UpstreamAccountID: 11,
		FiveSupported:     true, FiveUtilization: &five, FiveResetAt: &fiveReset,
		SevenSupported: true, SevenUtilization: &seven, SevenResetAt: &sevenReset,
		Source: "passive", SourceUpdatedAt: &sourceAt,
	}}, 1600); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPoolUsage(ctx, []PoolUsage{{
		UpstreamAccountID: 11, ErrorCode: "UPSTREAM_ACCOUNT_ERROR",
	}}, 1700); err != nil {
		t.Fatal(err)
	}

	accounts, err := store.ListPool(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	got := accounts[0]
	if !got.FiveSupported || got.FiveUtilization == nil || *got.FiveUtilization != five || got.FiveResetAt == nil || *got.FiveResetAt != fiveReset {
		t.Fatalf("5h snapshot was not preserved: %#v", got)
	}
	if !got.SevenSupported || got.SevenUtilization == nil || *got.SevenUtilization != seven || got.SevenResetAt == nil || *got.SevenResetAt != sevenReset {
		t.Fatalf("7d snapshot was not preserved: %#v", got)
	}
	if got.SourceUpdatedAt == nil || *got.SourceUpdatedAt != sourceAt || got.LastSuccessAt == nil || *got.LastSuccessAt != 1600 {
		t.Fatalf("successful snapshot timestamps changed after failure: %#v", got)
	}
	if got.LastErrorCode != "UPSTREAM_ACCOUNT_ERROR" || got.NormalizedStatus != "degraded" {
		t.Fatalf("failure state = %#v", got)
	}
}

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	box, err := secure.NewBox(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatalf("secure.NewBox() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "portal.db")
	store, err := Open(context.Background(), path, box)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store, path
}

func TestApplyKeyLimitChangePreservesIdentityAndRecomputesState(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	snapshot := compliantSnapshot(101)
	snapshot.Status = "inactive"
	user, err := store.CreateUser(ctx, "user", "User", "hash", &snapshot, 1)
	if err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	if user.Binding.BindingState != "upstream_inactive" {
		t.Fatalf("initial binding_state = %q, expected upstream_inactive", user.Binding.BindingState)
	}
	newLimit5h, newLimit7d := 15.5, 120.0
	newUsage5h, newUsage7d := 3.2, 45.1
	now := time.Now().Unix()
	reset5h, reset7d := now+3*3600, now+5*86400
	err = store.ApplyKeyLimitChange(ctx, 101, newLimit5h, newLimit7d, newUsage5h, newUsage7d,
		&reset5h, &reset7d, &now, now, 1)
	if err != nil {
		t.Fatalf("ApplyKeyLimitChange error = %v", err)
	}
	binding, err := store.BindingByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RateLimit5h != newLimit5h || binding.RateLimit7d != newLimit7d {
		t.Fatalf("limits after change = 5h:%v 7d:%v, want 5h:%v 7d:%v",
			binding.RateLimit5h, binding.RateLimit7d, newLimit5h, newLimit7d)
	}
	if binding.Usage5h != newUsage5h || binding.Usage7d != newUsage7d {
		t.Fatalf("usage after change = 5h:%v 7d:%v, want 5h:%v 7d:%v",
			binding.Usage5h, binding.Usage7d, newUsage5h, newUsage7d)
	}
	if binding.UpstreamStatus != "inactive" {
		t.Fatalf("upstream status changed to %q, should stay inactive", binding.UpstreamStatus)
	}
	// With both positive limits but upstream inactive, binding_state must be
	// upstream_inactive rather than healthy.
	if binding.BindingState != "upstream_inactive" {
		t.Fatalf("binding_state = %q after limit change, want upstream_inactive", binding.BindingState)
	}
	// Now simulate the next sync finding the key active again: the limit change
	// shouldn't have overridden binding_state permanently.
	snapshot.Status = "active"
	snapshot.RateLimit5h = newLimit5h
	snapshot.RateLimit7d = newLimit7d
	if err := store.ApplyKeySnapshots(ctx, []KeySnapshot{snapshot}, now+10); err != nil {
		t.Fatal(err)
	}
	binding, _ = store.BindingByUser(ctx, user.ID)
	if binding.BindingState != "healthy" {
		t.Fatalf("binding_state = %q after upstream reactivation, want healthy", binding.BindingState)
	}
}

func compliantSnapshot(id int64) KeySnapshot {
	return KeySnapshot{
		UpstreamKeyID: id,
		Name:          "key",
		Mask:          "sk-…abcd",
		Status:        "active",
		RateLimit5h:   10,
		Usage5h:       2,
		RateLimit7d:   100,
		Usage7d:       20,
	}
}

func createSessions(t *testing.T, store *Store, userID int64, tokenHashes ...string) {
	t.Helper()
	for _, tokenHash := range tokenHashes {
		if err := store.CreateSession(context.Background(), tokenHash, "csrf-"+tokenHash, userID, 100, 200, 300); err != nil {
			t.Fatalf("CreateSession(%q) error = %v", tokenHash, err)
		}
	}
}

func assertSessionCount(t *testing.T, store *Store, userID int64, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != want {
		t.Fatalf("session count for user %d = %d, want %d", userID, count, want)
	}
}

func assertFilesDoNotContain(t *testing.T, databasePath string, secrets []string) {
	t.Helper()
	paths, err := filepath.Glob(databasePath + "*")
	if err != nil {
		t.Fatalf("find SQLite files: %v", err)
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, secret := range secrets {
			if bytes.Contains(contents, []byte(secret)) {
				t.Fatalf("%s contains plaintext secret %q", path, secret)
			}
		}
	}
}
