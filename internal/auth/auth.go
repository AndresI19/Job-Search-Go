// Package auth verifies platform identity tokens. It is the Go counterpart of
// open-vMCP's server/src/auth/verify.ts: an RS256 JWT signed by platform-auth,
// checked against that service's JWKS endpoint, with the issuer and audience
// pinned. The verifier holds only the PUBLIC keys (fetched from JWKS, refreshed
// on an unknown kid so a rotated signing key is picked up without a redeploy), so
// it can check a token but never mint one.
//
// The "admin" role rides in the token as a SIGNED boolean claim — the same claim
// open-vMCP reads (`req.isAdmin = id.claims.admin === true`). This service cannot
// look the role up itself: the admin list is a secret platform-auth holds and we
// do not.
package auth

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Verifier checks bearer tokens and reports whether the caller is a platform
// admin. Construct it with FromEnv. A nil *Verifier is valid and means "auth not
// configured" (local dev): its IsAdmin is always false, and callers fall back to
// their own local trust model rather than gating on a token they cannot check.
type Verifier struct {
	jwks     keyfunc.Keyfunc // nil in a "deny-all" verifier (JWKS load failed)
	issuer   string
	audience string
}

// FromEnv builds a Verifier from AUTH_JWKS_URI, AUTH_ISSUER and AUTH_AUDIENCE
// (audience defaults to "platform"):
//
//   - AUTH_JWKS_URI unset  → (nil, nil): auth is not wired. This is the local-dev
//     signal, not an error — the caller keeps its own admin decision.
//   - JWKS loads           → (verifier, nil): the in-cluster happy path.
//   - JWKS fails to load    → (deny-all verifier, err): auth WAS requested but the
//     keys are unreachable. Returning a non-nil verifier is deliberate: the caller
//     must NOT silently fall back to local trust (that would open admin to a
//     spoofable request field), so a configured-but-broken auth locks admin
//     actions shut until a restart re-loads the keys.
func FromEnv(ctx context.Context) (*Verifier, error) {
	uri := os.Getenv("AUTH_JWKS_URI")
	if uri == "" {
		return nil, nil
	}
	v := &Verifier{
		issuer:   os.Getenv("AUTH_ISSUER"),
		audience: envOr("AUTH_AUDIENCE", "platform"),
	}
	k, err := keyfunc.NewDefaultCtx(ctx, []string{uri})
	if err != nil {
		return v, err // deny-all: jwks stays nil
	}
	v.jwks = k
	return v, nil
}

// IsAdmin reports whether the request carries a valid bearer token with a signed
// `admin: true` claim. Every failure — no token, bad signature, wrong
// issuer/audience, expired, `alg: none` — is "not admin", never an error: the safe
// direction, matching open-vMCP's identityMiddleware.
func (v *Verifier) IsAdmin(r *http.Request) bool {
	if v == nil || v.jwks == nil {
		return false
	}
	tok := bearer(r)
	if tok == "" {
		return false
	}

	opts := []jwt.ParserOption{
		// Pin RS256. Left open, the parser would honour whatever the token's header
		// asks for — including `none` — letting a forged header pick its own rules.
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(v.audience),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, v.jwks.Keyfunc, opts...)
	if err != nil || !parsed.Valid {
		return false
	}
	admin, _ := claims["admin"].(bool)
	return admin
}

// bearer extracts the token from an `Authorization: Bearer <token>` header, or ""
// if the header is absent or not a bearer.
func bearer(r *http.Request) string {
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
