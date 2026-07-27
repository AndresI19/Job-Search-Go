package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testKID = "test-key-1"

// jwksServer serves a single RSA public key as a JWKS, so keyfunc can fetch it
// exactly as it would from platform-auth's /.well-known/jwks.json.
func jwksServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	body, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID, "n": n, "e": e,
	}}})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func mint(t *testing.T, key *rsa.PrivateKey, method jwt.SigningMethod, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = testKID
	var signKey any = key
	if method == jwt.SigningMethodNone {
		signKey = jwt.UnsafeAllowNoneSignatureType
	}
	s, err := tok.SignedString(signKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func reqWith(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/run", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestIsAdmin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := jwksServer(t, &key.PublicKey)
	defer srv.Close()

	const iss = "https://api-andres.project-platform.me/auth"
	t.Setenv("AUTH_JWKS_URI", srv.URL)
	t.Setenv("AUTH_ISSUER", iss)
	t.Setenv("AUTH_AUDIENCE", "platform")

	v, err := FromEnv(context.Background())
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if v == nil {
		t.Fatal("FromEnv returned nil verifier despite AUTH_JWKS_URI set")
	}

	good := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss": iss, "aud": "platform", "sub": "u1", "admin": true,
			"exp": time.Now().Add(time.Hour).Unix(),
		}
	}

	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"valid admin", mint(t, key, jwt.SigningMethodRS256, good()), true},
		{"admin false", mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": "platform", "admin": false, "exp": time.Now().Add(time.Hour).Unix()}), false},
		{"admin claim absent", mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": "platform", "exp": time.Now().Add(time.Hour).Unix()}), false},
		{"wrong issuer", mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://evil.example", "aud": "platform", "admin": true, "exp": time.Now().Add(time.Hour).Unix()}), false},
		{"wrong audience", mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": "someone-else", "admin": true, "exp": time.Now().Add(time.Hour).Unix()}), false},
		{"expired", mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": "platform", "admin": true, "exp": time.Now().Add(-time.Hour).Unix()}), false},
		{"alg none", mint(t, key, jwt.SigningMethodNone, good()), false},
		{"no token", "", false},
		{"garbage", "not.a.jwt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := v.IsAdmin(reqWith(c.token)); got != c.want {
				t.Fatalf("IsAdmin = %v, want %v", got, c.want)
			}
		})
	}
}

// Identity returns the verified subject + admin flag; anonymous/invalid → ("", false).
func TestIdentity(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := jwksServer(t, &key.PublicKey)
	defer srv.Close()
	const iss = "iss"
	t.Setenv("AUTH_JWKS_URI", srv.URL)
	t.Setenv("AUTH_ISSUER", iss)
	t.Setenv("AUTH_AUDIENCE", "platform")
	v, err := FromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	tok := mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": iss, "aud": "platform", "sub": "user-abc", "admin": true,
		"exp": time.Now().Add(time.Hour).Unix()})
	if id, admin := v.Identity(reqWith(tok)); id != "user-abc" || !admin {
		t.Fatalf("Identity = (%q,%v), want (user-abc,true)", id, admin)
	}
	if id, admin := v.Identity(reqWith("")); id != "" || admin {
		t.Fatalf("anonymous Identity = (%q,%v), want (\"\",false)", id, admin)
	}
	// A non-admin user still gets their subject, just not admin.
	tok2 := mint(t, key, jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": iss, "aud": "platform", "sub": "plain-user",
		"exp": time.Now().Add(time.Hour).Unix()})
	if id, admin := v.Identity(reqWith(tok2)); id != "plain-user" || admin {
		t.Fatalf("non-admin Identity = (%q,%v), want (plain-user,false)", id, admin)
	}
}

// A second RSA key not present in the JWKS must fail: a token can only be trusted
// if it was signed by a key the JWKS actually publishes.
func TestIsAdminForeignKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := jwksServer(t, &key.PublicKey)
	defer srv.Close()

	t.Setenv("AUTH_JWKS_URI", srv.URL)
	t.Setenv("AUTH_ISSUER", "iss")
	t.Setenv("AUTH_AUDIENCE", "platform")
	v, err := FromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tok := mint(t, other, jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "iss", "aud": "platform", "admin": true, "exp": time.Now().Add(time.Hour).Unix()})
	if v.IsAdmin(reqWith(tok)) {
		t.Fatal("token signed by a key absent from the JWKS was accepted")
	}
}

// No AUTH_JWKS_URI → (nil, nil): auth not configured (local dev). A nil verifier's
// IsAdmin is always false, so callers fall back to their own local trust model.
func TestFromEnvUnconfigured(t *testing.T) {
	t.Setenv("AUTH_JWKS_URI", "")
	v, err := FromEnv(context.Background())
	if err != nil || v != nil {
		t.Fatalf("want (nil,nil) when unconfigured, got (%v,%v)", v, err)
	}
	if v.IsAdmin(reqWith("anything")) {
		t.Fatal("nil verifier must never report admin")
	}
}
