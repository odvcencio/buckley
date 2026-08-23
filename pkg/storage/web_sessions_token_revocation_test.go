package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateAuthSessionRequiresActiveSourceToken(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "source-token.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	source, err := store.CreateAPIToken("source", "alice", TokenScopeMember, "source-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAPIToken(source.ID); err != nil {
		t.Fatal(err)
	}
	for _, tokenID := range []string{source.ID, "missing-source"} {
		err := store.CreateAuthSession("session-"+tokenID, "alice", TokenScopeMember, tokenID, time.Now().Add(time.Hour))
		if !errors.Is(err, ErrAuthSessionSourceTokenInactive) {
			t.Fatalf("CreateAuthSession(%q) error = %v, want inactive source", tokenID, err)
		}
		session, getErr := store.GetAuthSession("session-" + tokenID)
		if getErr != nil || session != nil {
			t.Fatalf("inactive source created session %+v, err=%v", session, getErr)
		}
	}

	if err := store.CreateAuthSession("builtin-session", "builtin", TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("source-less session failed: %v", err)
	}
	if err := store.RevokeAPIToken(" \t "); err == nil {
		t.Fatal("empty API token revocation succeeded")
	}
	if session, err := store.GetAuthSession("builtin-session"); err != nil || session == nil || session.TokenID != "" {
		t.Fatalf("source-less session = %+v, err=%v", session, err)
	}
}

func TestGetAndCountAuthSessionsFailClosedOnInactiveSource(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "inactive-source-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	active, err := store.CreateAPIToken("active", "alice", TokenScopeMember, "runtime-active-secret")
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := store.CreateAPIToken("revoked", "bob", TokenScopeMember, "runtime-revoked-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, session := range []struct{ id, tokenID string }{
		{id: "runtime-active", tokenID: active.ID},
		{id: "runtime-revoked", tokenID: revoked.ID},
		{id: "runtime-missing", tokenID: "missing-token"},
		{id: "runtime-builtin"},
	} {
		if _, err := store.db.Exec(`
			INSERT INTO web_sessions (id, principal, scope, token_id, expires_at, created_at, last_seen_at)
			VALUES (?, 'user', 'member', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, session.id, session.tokenID, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE api_tokens SET revoked = 1 WHERE id = ?`, revoked.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"runtime-revoked", "runtime-missing"} {
		if session, err := store.GetAuthSession(id); err != nil || session != nil {
			t.Fatalf("inactive-source session %q = %+v, err=%v", id, session, err)
		}
	}
	for _, id := range []string{"runtime-active", "runtime-builtin"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("active session %q = %+v, err=%v", id, session, err)
		}
	}
	if count, err := store.CountActiveAuthSessions(now); err != nil || count != 2 {
		t.Fatalf("active auth session count=%d err=%v, want 2", count, err)
	}
}

func TestRevokeAPITokenDeletesOnlyDerivedWebSessions(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "revoke-cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.CreateAPIToken("first", "alice", TokenScopeMember, "first-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAPIToken("second", "bob", TokenScopeMember, "second-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []struct{ id, principal, tokenID string }{
		{id: "first-session", principal: "alice", tokenID: first.ID},
		{id: "second-session", principal: "bob", tokenID: second.ID},
		{id: "builtin-session", principal: "builtin"},
	} {
		if err := store.CreateAuthSession(session.id, session.principal, TokenScopeMember, session.tokenID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.RevokeAPIToken(first.ID); err != nil {
		t.Fatal(err)
	}
	if session, err := store.GetAuthSession("first-session"); err != nil || session != nil {
		t.Fatalf("revoked-token session = %+v, err=%v", session, err)
	}
	for _, id := range []string{"second-session", "builtin-session"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("unrelated session %q = %+v, err=%v", id, session, err)
		}
	}
}

func TestRevokeAPITokenRollsBackWhenSessionDeleteFails(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "revoke-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	secret := "rollback-secret"
	source, err := store.CreateAPIToken("rollback", "alice", TokenScopeMember, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAuthSession("rollback-session", "alice", TokenScopeMember, source.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER fail_token_session_delete
		BEFORE DELETE ON web_sessions
		WHEN OLD.token_id = '` + source.ID + `'
		BEGIN
			SELECT RAISE(ABORT, 'injected delete failure');
		END
	`); err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeAPIToken(source.ID); err == nil {
		t.Fatal("RevokeAPIToken succeeded despite failed session deletion")
	}
	validated, err := store.ValidateAPIToken(secret)
	if err != nil || validated == nil || validated.ID != source.ID {
		t.Fatalf("token revocation was not rolled back: token=%+v err=%v", validated, err)
	}
	if session, err := store.GetAuthSession("rollback-session"); err != nil || session == nil {
		t.Fatalf("session deletion was not rolled back: session=%+v err=%v", session, err)
	}
}

func TestDeleteAuthSessionsIsBoundedExactAndAtomic(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "batch-session-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{"batch-first", "batch-second", "batch-third"} {
		if err := store.CreateAuthSession(id, "builtin", TokenScopeOperator, "", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER fail_second_batch_session_delete
		BEFORE DELETE ON web_sessions
		WHEN OLD.id = 'batch-second'
		BEGIN
			SELECT RAISE(ABORT, 'injected second delete failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAuthSessions([]string{"batch-first", "batch-second"}); err == nil {
		t.Fatal("batch deletion succeeded despite second delete failure")
	}
	for _, id := range []string{"batch-first", "batch-second"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("rolled-back session %q = %+v, err=%v", id, session, err)
		}
	}
	for _, ids := range [][]string{
		{"batch-first", "batch-second", "batch-third"},
		{"batch-first", " batch-second"},
		{"batch-first", ""},
	} {
		if err := store.DeleteAuthSessions(ids); err == nil {
			t.Fatalf("DeleteAuthSessions(%q) succeeded", ids)
		}
	}
	for _, id := range []string{"batch-first", "batch-second", "batch-third"} {
		if session, err := store.GetAuthSession(id); err != nil || session == nil {
			t.Fatalf("invalid batch mutated session %q = %+v, err=%v", id, session, err)
		}
	}
}

func TestAuthSessionCreateRevokeSerializedAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-store-revoke.db")
	creator, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = creator.Close() })
	revoker, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = revoker.Close() })

	newSource := func(label string) *APIToken {
		t.Helper()
		token, err := creator.CreateAPIToken(label, "alice", TokenScopeMember, label+"-secret")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	assertNoSession := func(id, tokenID string) {
		t.Helper()
		if session, err := creator.GetAuthSession(id); err != nil || session != nil {
			t.Fatalf("session %q = %+v, err=%v", id, session, err)
		}
		var count int
		if err := creator.db.QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE token_id = ?`, tokenID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan count for %q = %d, err=%v", tokenID, count, err)
		}
	}

	revokeFirst := newSource("revoke-first")
	if err := revoker.RevokeAPIToken(revokeFirst.ID); err != nil {
		t.Fatal(err)
	}
	if err := creator.CreateAuthSession("revoke-first-session", "alice", TokenScopeMember, revokeFirst.ID, time.Now().Add(time.Hour)); !errors.Is(err, ErrAuthSessionSourceTokenInactive) {
		t.Fatalf("revoke-before-create error = %v", err)
	}
	assertNoSession("revoke-first-session", revokeFirst.ID)

	createFirst := newSource("create-first")
	if err := creator.CreateAuthSession("create-first-session", "alice", TokenScopeMember, createFirst.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := revoker.RevokeAPIToken(createFirst.ID); err != nil {
		t.Fatal(err)
	}
	assertNoSession("create-first-session", createFirst.ID)

	for i := 0; i < 32; i++ {
		source := newSource(fmt.Sprintf("concurrent-%d", i))
		sessionID := fmt.Sprintf("concurrent-session-%d", i)
		start := make(chan struct{})
		var createErr, revokeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			createErr = creator.CreateAuthSession(sessionID, "alice", TokenScopeMember, source.ID, time.Now().Add(time.Hour))
		}()
		go func() {
			defer wg.Done()
			<-start
			revokeErr = revoker.RevokeAPIToken(source.ID)
		}()
		close(start)
		wg.Wait()
		if createErr != nil && !errors.Is(createErr, ErrAuthSessionSourceTokenInactive) {
			t.Fatalf("iteration %d create error = %v", i, createErr)
		}
		if revokeErr != nil {
			t.Fatalf("iteration %d revoke error = %v", i, revokeErr)
		}
		assertNoSession(sessionID, source.ID)
	}
}

func TestWebSessionTokenIndexMigrationParity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-session-index.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_web_sessions_token_id'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("base schema token index count=%d err=%v", count, err)
	}
	revoked, err := store.CreateAPIToken("legacy-revoked", "alice", TokenScopeMember, "legacy-revoked-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []struct{ id, tokenID string }{
		{id: "legacy-revoked-session", tokenID: revoked.ID},
		{id: "legacy-missing-session", tokenID: "legacy-missing-token"},
		{id: "legacy-builtin-session"},
	} {
		if _, err := store.db.Exec(`
			INSERT INTO web_sessions (id, principal, scope, token_id, expires_at, created_at, last_seen_at)
			VALUES (?, 'legacy', 'member', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, session.id, session.tokenID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE api_tokens SET revoked = 1 WHERE id = ?`, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP INDEX idx_web_sessions_token_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version = 24`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_web_sessions_token_id'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migrated token index count=%d err=%v", count, err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE id IN ('legacy-revoked-session', 'legacy-missing-session')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("inactive legacy session count=%d err=%v", count, err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE id = 'legacy-builtin-session'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy builtin session count=%d err=%v", count, err)
	}
	var name string
	if err := reopened.db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 24`).Scan(&name); err != nil || name != "web_session_token_index" {
		t.Fatalf("migration 24 name=%q err=%v", name, err)
	}
}
