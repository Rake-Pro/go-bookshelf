package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/oidctest"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/internal/storetest"
)

// oidcHarness is a manager wired to a fake provider with a given group mapping.
type oidcHarness struct {
	mgr *auth.Manager
	db  *store.DB
	idp *oidctest.Provider
	ctx context.Context
}

func newOIDCHarness(t *testing.T, adminGroup, userGroup string) *oidcHarness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db := storetest.Open(t)
	auth.DefaultArgonParams = testParams()

	idp := oidctest.New(t, "bookshelf")
	cfg := config.Default()
	cfg.DBPath = filepath.Join(dir, "auth.db")
	mgr := auth.New(db, cfg)

	set := settings.Default()
	set.General.BaseURL = "https://books.example.com"
	set.OIDC = settings.OIDC{
		Enabled: true, Issuer: idp.URL(), ClientID: "bookshelf", ClientSecret: "client-secret",
		AdminGroup: adminGroup, UserGroup: userGroup, GroupsClaim: "groups",
		AutoRegister: true, LocalLoginEnabled: true,
	}
	apply, err := mgr.Prepare(ctx, set)
	if err != nil {
		t.Fatalf("prepare against the fake provider: %v", err)
	}
	apply()
	return &oidcHarness{mgr: mgr, db: db, idp: idp, ctx: ctx}
}

// signIn drives one complete callback for an identity in the given groups.
func (h *oidcHarness) signIn(t *testing.T, username string, groups []string) (*auth.User, error) {
	t.Helper()
	const state, nonce = "state-value", "nonce-value"
	h.idp.Issue(oidctest.Identity{
		Subject: "subject-" + username, Username: username,
		DisplayName: "A Reader", Groups: groups, Nonce: nonce,
	})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?"+url.Values{
		"state": {state}, "code": {"authorization-code"},
	}.Encode(), nil)
	r.AddCookie(&http.Cookie{Name: "gbs_oidc", Value: state + ":" + nonce})

	user, _, err := h.mgr.CompleteOIDC(h.ctx, httptest.NewRecorder(), r)
	return user, err
}

func (h *oidcHarness) userCount(t *testing.T) int {
	t.Helper()
	n, err := h.mgr.UserCount(h.ctx)
	if err != nil {
		t.Fatalf("user count: %v", err)
	}
	return n
}

// With a user group configured, membership of one of the two groups is the
// entry requirement and the group decides the role.
func TestOIDCGroupMapping(t *testing.T) {
	h := newOIDCHarness(t, "bookshelf-admins", "bookshelf-users")

	t.Run("member of the user group signs in as a user", func(t *testing.T) {
		u, err := h.signIn(t, "plain", []string{"bookshelf-users", "unrelated"})
		if err != nil {
			t.Fatalf("sign in: %v", err)
		}
		if u.Role != auth.RoleUser {
			t.Errorf("role = %q, want %q", u.Role, auth.RoleUser)
		}
	})

	t.Run("member of the admin group signs in as an admin", func(t *testing.T) {
		u, err := h.signIn(t, "boss", []string{"bookshelf-admins"})
		if err != nil {
			t.Fatalf("sign in: %v", err)
		}
		if u.Role != auth.RoleAdmin {
			t.Errorf("role = %q, want %q", u.Role, auth.RoleAdmin)
		}
	})

	t.Run("membership of both groups grants admin", func(t *testing.T) {
		u, err := h.signIn(t, "both", []string{"bookshelf-users", "bookshelf-admins"})
		if err != nil {
			t.Fatalf("sign in: %v", err)
		}
		if u.Role != auth.RoleAdmin {
			t.Errorf("role = %q, want %q", u.Role, auth.RoleAdmin)
		}
	})

	t.Run("member of neither group is refused and leaves no account", func(t *testing.T) {
		before := h.userCount(t)
		u, err := h.signIn(t, "outsider", []string{"someone-elses-app"})
		if !errors.Is(err, auth.ErrNotAuthorized) {
			t.Fatalf("sign in = %v, %v; want ErrNotAuthorized", u, err)
		}
		if after := h.userCount(t); after != before {
			t.Errorf("user count went from %d to %d; a refused identity must not create a row", before, after)
		}
	})

	t.Run("an absent groups claim is refused when a user group is required", func(t *testing.T) {
		if _, err := h.signIn(t, "claimless", nil); !errors.Is(err, auth.ErrNotAuthorized) {
			t.Errorf("sign in without a groups claim = %v, want ErrNotAuthorized", err)
		}
	})
}

// With no user group, the provider's own authentication is the only gate.
func TestOIDCWithoutUserGroupAdmitsEveryone(t *testing.T) {
	h := newOIDCHarness(t, "bookshelf-admins", "")

	u, err := h.signIn(t, "anyone", []string{"nothing-relevant"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if u.Role != auth.RoleUser {
		t.Errorf("role = %q, want %q", u.Role, auth.RoleUser)
	}

	admin, err := h.signIn(t, "boss", []string{"bookshelf-admins"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if admin.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want %q", admin.Role, auth.RoleAdmin)
	}
}

// The role follows the directory on every sign-in, in both directions.
func TestOIDCRoleIsReevaluated(t *testing.T) {
	h := newOIDCHarness(t, "bookshelf-admins", "bookshelf-users")

	promoted, err := h.signIn(t, "moving", []string{"bookshelf-users"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if promoted.Role != auth.RoleUser {
		t.Fatalf("first sign-in role = %q", promoted.Role)
	}

	promoted, err = h.signIn(t, "moving", []string{"bookshelf-admins"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if promoted.Role != auth.RoleAdmin {
		t.Errorf("after joining the admin group, role = %q, want admin", promoted.Role)
	}

	demoted, err := h.signIn(t, "moving", []string{"bookshelf-users"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if demoted.Role != auth.RoleUser {
		t.Errorf("after leaving the admin group, role = %q, want user", demoted.Role)
	}
}

// The break-glass administrator - the one with a local password - keeps the
// role even if the directory stops saying they should have it. Demoting them
// would remove the way back in exactly when the directory is what broke.
func TestOIDCDoesNotDemoteThePasswordAdmin(t *testing.T) {
	h := newOIDCHarness(t, "bookshelf-admins", "bookshelf-users")

	local, err := h.mgr.CreateUser(h.ctx, "owner", "correct-horse-battery", "Owner", auth.RoleAdmin)
	if err != nil {
		t.Fatalf("create local admin: %v", err)
	}

	signedIn, err := h.signIn(t, "owner", []string{"bookshelf-users"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if signedIn.ID != local.ID {
		t.Fatalf("the sign-in adopted a different account (%d, want %d)", signedIn.ID, local.ID)
	}
	if signedIn.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want the password administrator to keep admin", signedIn.Role)
	}
}

// A restricted account is a local decision the directory knows nothing about.
func TestOIDCLeavesRestrictedAccountsAlone(t *testing.T) {
	h := newOIDCHarness(t, "bookshelf-admins", "")

	if _, err := h.mgr.CreateUser(h.ctx, "limited", "", "Limited", auth.RoleRestricted); err != nil {
		t.Fatalf("create restricted user: %v", err)
	}
	u, err := h.signIn(t, "limited", []string{"bookshelf-admins"})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if u.Role != auth.RoleRestricted {
		t.Errorf("role = %q, want the restricted role to survive", u.Role)
	}
}

// With automatic registration off, a verified identity still needs an account
// somebody made for it.
func TestOIDCAutoRegisterOff(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	auth.DefaultArgonParams = testParams()

	idp := oidctest.New(t, "bookshelf")
	cfg := config.Default()
	mgr := auth.New(db, cfg)
	set := settings.Default()
	set.General.BaseURL = "https://books.example.com"
	set.OIDC = settings.OIDC{
		Enabled: true, Issuer: idp.URL(), ClientID: "bookshelf", ClientSecret: "client-secret",
		GroupsClaim: "groups", AutoRegister: false, LocalLoginEnabled: true,
	}
	apply, err := mgr.Prepare(ctx, set)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	apply()

	h := &oidcHarness{mgr: mgr, db: db, idp: idp, ctx: ctx}
	if _, err := h.signIn(t, "stranger", nil); !errors.Is(err, auth.ErrNoAccount) {
		t.Fatalf("sign in with auto-registration off = %v, want ErrNoAccount", err)
	}
	if n := h.userCount(t); n != 0 {
		t.Errorf("user count = %d, want no account created", n)
	}
}

// A settings save whose issuer does not answer discovery must be reported as
// an error rather than quietly stored.
func TestPrepareReportsDiscoveryFailure(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)

	mgr := auth.New(db, config.Default())
	set := settings.Default()
	set.OIDC = settings.OIDC{
		Enabled: true, Issuer: "http://127.0.0.1:1/nowhere", ClientID: "bookshelf",
		ClientSecret: "x", GroupsClaim: "groups", LocalLoginEnabled: true,
	}
	apply, err := mgr.Prepare(ctx, set)
	if err == nil {
		t.Fatal("discovery against a dead issuer must fail")
	}
	// The rest of the configuration still applies, so a provider that is down
	// at startup does not also take local sign-in with it.
	apply()
	if mgr.OIDCEnabled() {
		t.Error("OIDC must stay off after a failed discovery")
	}
	if !mgr.LocalLoginEnabled() {
		t.Error("local sign-in must survive a failed discovery")
	}
}
