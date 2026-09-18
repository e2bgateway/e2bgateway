// auth-fixture creates throwaway signed JWTs and a public JWKS for Kind E2E.
// It never outputs the private signing key.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	jwks, err := json.Marshal(map[string]any{
		"keys": []map[string]string{{"kty": "RSA", "kid": "kind-e2e-key", "alg": "RS256", "use": "sig", "n": n, "e": e}},
	})
	if err != nil {
		log.Fatal(err)
	}
	sign := func(subject, scope string, expiry time.Time) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"sub": subject, "scope": scope, "iss": "https://kind-e2e-issuer.example.test",
			"aud": "e2bgateway-kind-e2e", "exp": expiry.Unix(),
		})
		token.Header["kid"] = "kind-e2e-key"
		raw, signErr := token.SignedString(key)
		if signErr != nil {
			log.Fatal(signErr)
		}
		return raw
	}
	result := map[string]string{
		"jwks":         string(jwks),
		"valid":        sign("reader", "sandbox:read", time.Now().Add(time.Hour)),
		"insufficient": sign("forbidden", "template:read", time.Now().Add(time.Hour)),
		"expired":      sign("expired", "sandbox:read", time.Now().Add(-time.Minute)),
		"rate":         sign("rate", "sandbox:read", time.Now().Add(time.Hour)),
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		log.Fatal(err)
	}
}
