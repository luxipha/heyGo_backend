package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("invalid CasperID token")

type Identity struct {
	Subject       string
	HumanID       string
	Email         string
	EmailVerified bool
	Verified      bool
	KYCTier       string
	TrustScore    *int
	Scopes        []string
}

type Verifier interface {
	Verify(context.Context, string) (Identity, error)
}

type CasperIDVerifier struct {
	jwksURL    string
	issuer     string
	audience   string
	httpClient *http.Client
	cacheTTL   time.Duration

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewCasperIDVerifier(jwksURL, issuer, audience string) *CasperIDVerifier {
	return &CasperIDVerifier{
		jwksURL:    jwksURL,
		issuer:     issuer,
		audience:   audience,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		cacheTTL:   15 * time.Minute,
		keys:       make(map[string]*rsa.PublicKey),
	}
}

func (v *CasperIDVerifier) Ping(ctx context.Context) error { return v.refresh(ctx) }

func (v *CasperIDVerifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	rawToken = strings.TrimSpace(strings.TrimPrefix(rawToken, "Bearer "))
	if rawToken == "" {
		return Identity{}, ErrInvalidToken
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("%w: unsupported signing algorithm", ErrInvalidToken)
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("%w: missing key id", ErrInvalidToken)
		}
		return v.key(ctx, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	if claimString(claims, "token_type") != "business" || claimString(claims, "app_id") != v.audience {
		return Identity{}, fmt.Errorf("%w: token is not issued for this application", ErrInvalidToken)
	}

	subject, err := claims.GetSubject()
	if err != nil || subject == "" {
		return Identity{}, fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}
	humanID := claimString(claims, "humanId")
	if humanID == "" {
		return Identity{}, fmt.Errorf("%w: humanId was not shared", ErrInvalidToken)
	}

	return Identity{
		Subject:       subject,
		HumanID:       humanID,
		Email:         claimString(claims, "email"),
		EmailVerified: claimBool(claims, "email_verified"),
		Verified:      claimBool(claims, "verified"),
		KYCTier:       claimString(claims, "tier"),
		TrustScore:    claimInt(claims, "trust_score"),
		Scopes:        claimStrings(claims, "scopes"),
	}, nil
}

func claimInt(claims jwt.MapClaims, name string) *int {
	switch value := claims[name].(type) {
	case float64:
		integer := int(value)
		return &integer
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			integer := int(parsed)
			return &integer
		}
	}
	return nil
}

func (v *CasperIDVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, exists := v.keys[kid]
	fresh := time.Since(v.fetchedAt) < v.cacheTTL
	v.mu.RUnlock()
	if exists && fresh {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		if exists {
			return key, nil
		}
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	key, exists = v.keys[kid]
	if !exists {
		return nil, fmt.Errorf("%w: unknown key id", ErrInvalidToken)
	}
	return key, nil
}

func (v *CasperIDVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("create JWKS request: %w", err)
	}
	res, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: unexpected status %d", res.StatusCode)
	}

	var set struct {
		Keys []struct {
			KID string `json:"kid"`
			KTY string `json:"kty"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, item := range set.Keys {
		if item.KID == "" || item.KTY != "RSA" || item.Alg != "RS256" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(item.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(item.E)
		if err != nil {
			continue
		}
		exponent := new(big.Int).SetBytes(e).Int64()
		if exponent <= 0 {
			continue
		}
		keys[item.KID] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}
	}
	if len(keys) == 0 {
		return fmt.Errorf("fetch JWKS: no usable RS256 keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

func claimString(claims jwt.MapClaims, name string) string {
	switch value := claims[name].(type) {
	case string:
		return value
	case float64:
		return fmt.Sprintf("%g", value)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func claimBool(claims jwt.MapClaims, name string) bool {
	value, _ := claims[name].(bool)
	return value
}

func claimStrings(claims jwt.MapClaims, name string) []string {
	values, _ := claims[name].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
