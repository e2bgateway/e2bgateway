package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/e2bgateway/e2bgateway/internal/config"
)

const maxJWKSBytes = 1 << 20

// JWTProvider validates signed JWTs against public keys configured at startup.
type JWTProvider struct {
	cfg  config.AuthProviderConfig
	keys keyfunc.Keyfunc
}

type gatewayClaims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

// NewJWTProvider validates static public keys before accepting requests.
func NewJWTProvider(cfg config.AuthProviderConfig) (*JWTProvider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	keys, err := parseStaticJWKS(cfg.JWKS)
	if err != nil {
		return nil, err
	}
	return &JWTProvider{cfg: cfg, keys: keys}, nil
}

func (p *JWTProvider) Name() string { return "jwt" }

// Authenticate checks the signature and claims before using identity or scopes.
func (p *JWTProvider) Authenticate(r *http.Request) (*TenantContext, error) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return nil, fmt.Errorf("missing bearer token")
	}
	raw := strings.TrimPrefix(authorization, "Bearer ")
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("invalid bearer token")
	}

	claims := &gatewayClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, p.verificationKey,
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg(), jwt.SigningMethodES256.Alg()}),
		jwt.WithIssuer(p.cfg.Issuer),
		jwt.WithAudience(p.cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(p.cfg.ClockSkew),
	)
	if err != nil || token == nil || !token.Valid || claims.Subject == "" {
		return nil, fmt.Errorf("invalid JWT")
	}

	return &TenantContext{TenantID: claims.Subject, Scopes: strings.Fields(claims.Scope)}, nil
}

func (p *JWTProvider) verificationKey(token *jwt.Token) (any, error) {
	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("missing key ID")
	}
	return p.keys.Keyfunc(token)
}

func parseStaticJWKS(raw string) (keyfunc.Keyfunc, error) {
	if len(raw) > maxJWKSBytes {
		return nil, fmt.Errorf("static jwks is too large")
	}
	var document struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal([]byte(raw), &document); err != nil || len(document.Keys) == 0 {
		return nil, fmt.Errorf("static jwks must contain public keys")
	}
	seen := make(map[string]struct{}, len(document.Keys))
	for _, key := range document.Keys {
		if err := validatePublicJWK(key, seen); err != nil {
			return nil, err
		}
	}
	keys, err := keyfunc.NewJWKSetJSON([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid static jwks: %w", err)
	}
	return keys, nil
}

func validatePublicJWK(key map[string]json.RawMessage, seen map[string]struct{}) error {
	kid, kty, alg := jwkField(key, "kid"), jwkField(key, "kty"), jwkField(key, "alg")
	if kid == "" || strings.TrimSpace(kid) != kid {
		return fmt.Errorf("static jwks key needs a valid kid")
	}
	if _, exists := seen[kid]; exists {
		return fmt.Errorf("static jwks contains duplicate kid")
	}
	seen[kid] = struct{}{}
	if (alg != "RS256" || kty != "RSA") && (alg != "ES256" || kty != "EC" || jwkField(key, "crv") != "P-256") {
		return fmt.Errorf("static jwks key has unsupported algorithm or type")
	}
	if use := jwkField(key, "use"); use != "" && use != "sig" {
		return fmt.Errorf("static jwks key is not for signatures")
	}
	for _, privateField := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
		if _, exists := key[privateField]; exists {
			return fmt.Errorf("static jwks must contain public keys only")
		}
	}
	return nil
}

func jwkField(key map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(key[name], &value)
	return value
}
