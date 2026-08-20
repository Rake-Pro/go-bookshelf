package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rs/zerolog/log"
)

// discoveryTimeout bounds the probe run when an issuer is saved or tested, so
// an unreachable URL fails the request quickly instead of hanging it.
const discoveryTimeout = 15 * time.Second

// settingsResponse is what GET /admin/settings answers with.
//
// The client secret is reported only as "stored or not". It is the one value
// the admin page can write but never read back, which is what keeps a stolen
// admin session from also being a stolen client secret.
type settingsResponse struct {
	General struct {
		BaseURL       string `json:"base_url"`
		SecureCookies string `json:"secure_cookies"`
		SessionTTL    string `json:"session_ttl"`
		ScanInterval  string `json:"scan_interval"`
	} `json:"general"`
	OIDC struct {
		Enabled           bool   `json:"enabled"`
		Issuer            string `json:"issuer"`
		ClientID          string `json:"client_id"`
		HasClientSecret   bool   `json:"has_client_secret"`
		AdminGroup        string `json:"admin_group"`
		UserGroup         string `json:"user_group"`
		GroupsClaim       string `json:"groups_claim"`
		Scopes            string `json:"scopes"`
		AutoRegister      bool   `json:"auto_register"`
		LocalLoginEnabled bool   `json:"local_login_enabled"`
		RedirectURL       string `json:"redirect_url"`
		Active            bool   `json:"active"`
	} `json:"oidc"`
	ProxyAuth struct {
		Enabled        bool     `json:"enabled"`
		Header         string   `json:"header"`
		TrustedProxies []string `json:"trusted_proxies"`
	} `json:"proxy_auth"`
	Metadata struct {
		Provider     string `json:"provider"`
		AllowPrivate bool   `json:"allow_private"`
	} `json:"metadata"`
	Metrics struct {
		Allow []string `json:"allow"`
	} `json:"metrics"`

	SetupComplete bool   `json:"setup_complete"`
	UpdatedAt     string `json:"updated_at"`

	// AdminRecovery mirrors GOBOOKSHELF_ADMIN_RECOVERY. The page shows it so an
	// operator understands why the password form is still on offer after they
	// turned it off.
	AdminRecovery bool `json:"admin_recovery"`
}

// oidcRequest is the writable half of the OIDC section. It is shared with the
// wizard so both surfaces accept the same body.
type oidcRequest struct {
	Enabled     bool   `json:"enabled"`
	Issuer      string `json:"issuer"`
	ClientID    string `json:"client_id"`
	AdminGroup  string `json:"admin_group"`
	UserGroup   string `json:"user_group"`
	GroupsClaim string `json:"groups_claim"`
	Scopes      string `json:"scopes"`

	// ClientSecret empty means "keep the stored one", so a form that never
	// received the secret can be saved without wiping it. Clearing it is done
	// by turning OIDC off.
	ClientSecret string `json:"client_secret"`

	AutoRegister      *bool `json:"auto_register"`
	LocalLoginEnabled *bool `json:"local_login_enabled"`
}

// applyTo folds the request onto a settings document, preserving the stored
// client secret when none was sent.
func (o oidcRequest) applyTo(dst *settings.Settings) {
	kept := dst.OIDC.ClientSecret
	dst.OIDC.Enabled = o.Enabled
	dst.OIDC.Issuer = o.Issuer
	dst.OIDC.ClientID = o.ClientID
	dst.OIDC.AdminGroup = o.AdminGroup
	dst.OIDC.UserGroup = o.UserGroup
	dst.OIDC.GroupsClaim = o.GroupsClaim
	dst.OIDC.Scopes = o.Scopes
	if strings.TrimSpace(o.ClientSecret) == "" {
		dst.OIDC.ClientSecret = kept
	} else {
		dst.OIDC.ClientSecret = strings.TrimSpace(o.ClientSecret)
	}
	if o.AutoRegister != nil {
		dst.OIDC.AutoRegister = *o.AutoRegister
	}
	if o.LocalLoginEnabled != nil {
		dst.OIDC.LocalLoginEnabled = *o.LocalLoginEnabled
	}
}

// settingsRequest is the body of PUT /admin/settings. Every section is a
// pointer so a page that edits one card does not have to resend the rest.
type settingsRequest struct {
	General *struct {
		BaseURL       *string `json:"base_url"`
		SecureCookies *string `json:"secure_cookies"`
		SessionTTL    *string `json:"session_ttl"`
		ScanInterval  *string `json:"scan_interval"`
	} `json:"general"`
	OIDC      *oidcRequest `json:"oidc"`
	ProxyAuth *struct {
		Enabled        *bool    `json:"enabled"`
		Header         *string  `json:"header"`
		TrustedProxies []string `json:"trusted_proxies"`
	} `json:"proxy_auth"`
	Metadata *struct {
		Provider     *string `json:"provider"`
		AllowPrivate *bool   `json:"allow_private"`
	} `json:"metadata"`
	Metrics *struct {
		Allow []string `json:"allow"`
	} `json:"metrics"`
}

// getAdminSettings is GET /api/v1/admin/settings.
func (a *API) getAdminSettings(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	writeJSON(w, http.StatusOK, a.settingsBody())
}

// putAdminSettings is PUT /api/v1/admin/settings. The save is all or nothing:
// a rejected section leaves the running server exactly as it was.
func (a *API) putAdminSettings(w http.ResponseWriter, r *http.Request) {
	admin := requireAdmin(w, r)
	if admin == nil {
		return
	}
	var body settingsRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	next := a.settings.Get()
	if g := body.General; g != nil {
		if g.BaseURL != nil {
			next.General.BaseURL = *g.BaseURL
		}
		if g.SecureCookies != nil {
			next.General.SecureCookies = strings.ToLower(strings.TrimSpace(*g.SecureCookies))
		}
		if g.SessionTTL != nil {
			d, err := time.ParseDuration(strings.TrimSpace(*g.SessionTTL))
			if err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest,
					"session lifetime must be a duration such as \"720h\"")
				return
			}
			next.General.SessionTTL = settings.Duration(d)
		}
		if g.ScanInterval != nil {
			d, err := time.ParseDuration(strings.TrimSpace(*g.ScanInterval))
			if err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest,
					"scan interval must be a duration such as \"6h\"")
				return
			}
			next.General.ScanInterval = settings.Duration(d)
		}
	}
	if body.OIDC != nil {
		body.OIDC.applyTo(&next)
	}
	if p := body.ProxyAuth; p != nil {
		if p.Enabled != nil {
			next.ProxyAuth.Enabled = *p.Enabled
		}
		if p.Header != nil {
			next.ProxyAuth.Header = *p.Header
		}
		if p.TrustedProxies != nil {
			next.ProxyAuth.TrustedProxies = p.TrustedProxies
		}
	}
	if m := body.Metadata; m != nil {
		if m.Provider != nil {
			next.Metadata.Provider = *m.Provider
		}
		if m.AllowPrivate != nil {
			next.Metadata.AllowPrivate = *m.AllowPrivate
		}
	}
	if m := body.Metrics; m != nil && m.Allow != nil {
		next.Metrics.Allow = m.Allow
	}

	// Turning the password form off has one more condition than validation can
	// see: there has to be somebody who could still sign in with OIDC. The
	// administrator doing it is the obvious candidate, so require that their
	// own account is already linked to an identity, or that a group grants the
	// role automatically.
	if !next.OIDC.LocalLoginEnabled && next.OIDC.Enabled && next.OIDC.AdminGroup == "" {
		linked, err := a.auth.AdminsWithOIDC(r.Context())
		if err != nil {
			fail(w, err, "settings")
			return
		}
		if linked == 0 {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				"sign in through the identity provider once, or set an admin group, "+
					"before turning the password form off; otherwise no administrator could get back in")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), discoveryTimeout)
	defer cancel()
	if err := a.saveSettings(w, r.WithContext(ctx), next); err != nil {
		return
	}
	log.Info().Str("admin", admin.User.Username).Msg("settings updated")
	writeJSON(w, http.StatusOK, a.settingsBody())
}

// testAdminOIDC is POST /api/v1/admin/settings/oidc/test: run discovery
// against a candidate issuer and report what happened, changing nothing.
func (a *API) testAdminOIDC(w http.ResponseWriter, r *http.Request) {
	if requireAdmin(w, r) == nil {
		return
	}
	var body oidcRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	probe := a.settings.Get()
	body.Enabled = true
	body.applyTo(&probe)
	probe.Normalize()
	if err := probe.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": strings.TrimPrefix(err.Error(), "settings: "),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), discoveryTimeout)
	defer cancel()
	if err := auth.Discover(ctx, probe); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"issuer":       probe.OIDC.Issuer,
		"redirect_url": probe.OIDCRedirectURL(),
		// Discovery proves the issuer answers; it says nothing about what a
		// token will carry. Echo the group mapping so the operator can check it
		// against the claim their provider actually issues.
		"groups_claim": probe.OIDC.GroupsClaim,
		"admin_group":  probe.OIDC.AdminGroup,
		"user_group":   probe.OIDC.UserGroup,
	})
}

// settingsBody renders the current settings for a client.
func (a *API) settingsBody() settingsResponse {
	cur := a.settings.Get()
	var out settingsResponse

	out.General.BaseURL = cur.General.BaseURL
	out.General.SecureCookies = cur.General.SecureCookies
	out.General.SessionTTL = cur.General.SessionTTL.String()
	out.General.ScanInterval = cur.General.ScanInterval.String()

	out.OIDC.Enabled = cur.OIDC.Enabled
	out.OIDC.Issuer = cur.OIDC.Issuer
	out.OIDC.ClientID = cur.OIDC.ClientID
	out.OIDC.HasClientSecret = cur.HasClientSecret()
	out.OIDC.AdminGroup = cur.OIDC.AdminGroup
	out.OIDC.UserGroup = cur.OIDC.UserGroup
	out.OIDC.GroupsClaim = cur.OIDC.GroupsClaim
	out.OIDC.Scopes = cur.OIDC.Scopes
	out.OIDC.AutoRegister = cur.OIDC.AutoRegister
	out.OIDC.LocalLoginEnabled = cur.OIDC.LocalLoginEnabled
	out.OIDC.RedirectURL = cur.OIDCRedirectURL()
	// Enabled is what is stored; Active is whether discovery actually
	// succeeded, which is the difference between "configured" and "working".
	out.OIDC.Active = a.auth.OIDCEnabled()

	out.ProxyAuth.Enabled = cur.ProxyAuth.Enabled
	out.ProxyAuth.Header = cur.ProxyAuth.Header
	out.ProxyAuth.TrustedProxies = nonNil(cur.ProxyAuth.TrustedProxies)

	out.Metadata.Provider = cur.Metadata.Provider
	out.Metadata.AllowPrivate = cur.Metadata.AllowPrivate
	out.Metrics.Allow = nonNil(cur.Metrics.Allow)

	out.SetupComplete = cur.SetupComplete
	out.UpdatedAt = cur.UpdatedAt
	out.AdminRecovery = a.cfg.AdminRecovery
	return out
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
