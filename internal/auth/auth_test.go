package auth_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
)

func testParams() auth.ArgonParams {
	return auth.ArgonParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
}

// newManager builds a manager over a fresh database with test-cost hashing.
func newManager(t *testing.T) (*auth.Manager, *store.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db := storetest.Open(t)

	auth.DefaultArgonParams = testParams()
	cfg := config.Default()
	cfg.DBPath = filepath.Join(dir, "auth.db")
	mgr := auth.New(db, cfg)
	apply, err := mgr.Prepare(ctx, settings.Default())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	apply()
	return mgr, db, ctx
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery", testParams())
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q, want a PHC argon2id string", hash)
	}
	if strings.Contains(hash, "correct-horse-battery") {
		t.Fatal("the hash contains the password")
	}

	ok, err := auth.VerifyPassword("correct-horse-battery", hash)
	if err != nil || !ok {
		t.Fatalf("verify correct password = %v, %v", ok, err)
	}
	ok, err = auth.VerifyPassword("wrong", hash)
	if err != nil || ok {
		t.Fatalf("verify wrong password = %v, %v", ok, err)
	}

	// Each hash gets its own salt.
	other, err := auth.HashPassword("correct-horse-battery", testParams())
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Fatal("two hashes of the same password are identical; the salt is not random")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext",
		"$argon2i$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$bad$c2FsdA$aGFzaA",
	} {
		if ok, err := auth.VerifyPassword("x", bad); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want a rejection", bad, ok, err)
		}
	}
}

func TestLimiterBurstAndReset(t *testing.T) {
	l := auth.NewLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d was refused inside the burst", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("the fourth attempt should be refused")
	}
	if !l.Allow("5.6.7.8") {
		t.Error("a different address must have its own bucket")
	}
	l.Reset("1.2.3.4")
	if !l.Allow("1.2.3.4") {
		t.Error("a reset bucket should allow again")
	}
}

func TestSetupTokenIsSingleUseAndHashed(t *testing.T) {
	mgr, db, ctx := newManager(t)

	required, err := mgr.SetupRequired(ctx)
	if err != nil || !required {
		t.Fatalf("setup required = %v, %v", required, err)
	}
	token, err := mgr.EnsureSetupToken(ctx)
	if err != nil || token == "" {
		t.Fatalf("token = %q, %v", token, err)
	}

	var stored string
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM setup_state WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("the setup token is stored in clear")
	}

	if _, err := mgr.Setup(ctx, "wrong-token", "admin", "correct-horse-battery", "Admin", "127.0.0.1"); err == nil {
		t.Fatal("setup accepted a wrong token")
	}
	user, err := mgr.Setup(ctx, token, "admin", "correct-horse-battery", "Admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if user.Role != auth.RoleAdmin {
		t.Errorf("first account role = %q, want admin", user.Role)
	}
	if _, err := mgr.Setup(ctx, token, "second", "correct-horse-battery", "Second", "127.0.0.1"); err == nil {
		t.Fatal("setup ran twice")
	}
	again, err := mgr.EnsureSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Error("a setup token was issued after setup completed")
	}
}

// Concurrent setup attempts must produce exactly one administrator. The
// "no accounts yet" check is not enough on its own - two requests can both pass
// it - so the token is claimed with a conditional update that only one of them
// can win.
func TestSetupTokenIsSpentExactlyOnceUnderConcurrency(t *testing.T) {
	mgr, _, ctx := newManager(t)

	token, err := mgr.EnsureSetupToken(ctx)
	if err != nil || token == "" {
		t.Fatalf("token = %q, %v", token, err)
	}

	const attempts = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created []string
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A username each, so the unique index is not what serialises them.
			name := fmt.Sprintf("admin%d", i)
			u, err := mgr.Setup(ctx, token, name, "correct-horse-battery", name, "127.0.0.1")
			if err != nil {
				return
			}
			mu.Lock()
			created = append(created, u.Username)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(created) != 1 {
		t.Fatalf("setup created %d administrators (%v), want exactly 1", len(created), created)
	}
	users, err := mgr.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Errorf("users table holds %d rows, want 1", len(users))
	}
}

// A rejected account must not burn the token: the operator has only the one,
// and a password the server refuses is their mistake to correct, not a reason
// to restart the server.
func TestSetupTokenSurvivesARejectedAccount(t *testing.T) {
	mgr, _, ctx := newManager(t)

	token, err := mgr.EnsureSetupToken(ctx)
	if err != nil || token == "" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if _, err := mgr.Setup(ctx, token, "admin", "short", "Admin", "127.0.0.1"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("short password = %v, want ErrWeakPassword", err)
	}
	if _, err := mgr.Setup(ctx, token, "admin", "correct-horse-battery", "Admin", "127.0.0.1"); err != nil {
		t.Fatalf("the token was spent by the rejected attempt: %v", err)
	}
}

func TestSessionExpiryPruned(t *testing.T) {
	mgr, db, ctx := newManager(t)
	user, err := mgr.CreateUser(ctx, "reader", "correct-horse-battery", "Reader", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := mgr.CreateSession(ctx, user.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`,
		store.FormatTime(time.Now().Add(-time.Minute)), sid); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PruneSessions(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("expired sessions remaining = %d", remaining)
	}
}

func TestDisabledAccountCannotLogIn(t *testing.T) {
	mgr, _, ctx := newManager(t)
	user, err := mgr.CreateUser(ctx, "reader", "correct-horse-battery", "Reader", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Login(ctx, "reader", "correct-horse-battery", "test", "127.0.0.1"); err == nil {
		t.Fatal("a disabled account logged in")
	}
}

func TestPasswordlessAccountCannotLogIn(t *testing.T) {
	mgr, _, ctx := newManager(t)
	// An account provisioned through OIDC or a trusted proxy has no password.
	if _, err := mgr.CreateUser(ctx, "federated", "", "Federated", auth.RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Login(ctx, "federated", "", "test", "127.0.0.1"); err == nil {
		t.Fatal("an empty password was accepted for a passwordless account")
	}
	if _, _, err := mgr.Login(ctx, "federated", "anything", "test", "127.0.0.1"); err == nil {
		t.Fatal("a passwordless account accepted a password")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	mgr, _, ctx := newManager(t)
	if _, err := mgr.CreateUser(ctx, "reader", "short", "Reader", auth.RoleUser); err == nil {
		t.Fatal("a password below the minimum length was accepted")
	}
}

func TestTokenScopesDefaultToRead(t *testing.T) {
	mgr, _, ctx := newManager(t)
	user, err := mgr.CreateUser(ctx, "reader", "correct-horse-battery", "Reader", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	token, secret, err := mgr.CreateToken(ctx, user.ID, "app", []string{"nonsense"})
	if err != nil {
		t.Fatal(err)
	}
	if token.Scopes != auth.ScopeRead {
		t.Errorf("scopes = %q, want the read fallback", token.Scopes)
	}
	if !strings.HasPrefix(secret, auth.TokenPrefix) {
		t.Errorf("secret = %q, want the token prefix", secret)
	}
	// Only the hash is stored: the secret cannot be recovered from a listing.
	list, err := mgr.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("tokens = %d", len(list))
	}
}
