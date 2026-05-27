package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// OIDCAuth implements OIDC token validation (like tdoctl).
type OIDCAuth struct {
	issuerURL        string
	audience         string
	allowedGroups    []string
	jwksCacheTime    int
	skipIssuerVerify bool
	logger           *slog.Logger
	jwksCache        map[string]*rsa.PublicKey
	jwksCacheExp     time.Time
}

// OIDCConfig holds OIDC configuration.
type OIDCConfig struct {
	IssuerURL        string
	Audience         string
	AllowedGroups    []string
	JWKSCacheTime    int // seconds
	SkipIssuerVerify bool
}

// JWK represents a JSON Web Key.
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKSResponse represents the JWKS endpoint response.
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// NewOIDCAuth creates a new OIDC authenticator.
func NewOIDCAuth(config *OIDCConfig, logger *slog.Logger) (auth *OIDCAuth) {
	if config.JWKSCacheTime == 0 {
		config.JWKSCacheTime = 300 // 5 minutes default
	}

	auth = &OIDCAuth{
		issuerURL:        config.IssuerURL,
		audience:         config.Audience,
		allowedGroups:    config.AllowedGroups,
		jwksCacheTime:    config.JWKSCacheTime,
		skipIssuerVerify: config.SkipIssuerVerify,
		logger:           logger,
		jwksCache:        make(map[string]*rsa.PublicKey),
	}
	return auth
}

// Name returns the auth method name.
func (a *OIDCAuth) Name() (name string) {
	name = "oidc"
	return name
}

// Authenticate validates an OIDC token and returns user claims.
//
//nolint:gocognit,funlen // Token validation requires multiple validation steps
func (a *OIDCAuth) Authenticate(r *http.Request) (result *Result, err error) {
	var tokenString string
	tokenString, err = extractBearerToken(r)
	if err != nil {
		return result, err
	}

	// Parse token without verification to get header
	parser := &jwt.Parser{}
	var token *jwt.Token
	token, _, err = parser.ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		err = fmt.Errorf("failed to parse token: %w", err)
		return result, err
	}

	// Get key ID from header
	kid, ok := token.Header["kid"].(string)
	if !ok {
		err = errors.New("missing kid in token header")
		return result, err
	}

	// Get public key for verification
	var publicKey *rsa.PublicKey
	publicKey, err = a.getPublicKey(kid)
	if err != nil {
		err = fmt.Errorf("failed to get public key: %w", err)
		return result, err
	}

	// Parse and validate token with public key
	var validatedToken *jwt.Token
	validatedToken, err = jwt.Parse(tokenString, func(token *jwt.Token) (key interface{}, keyErr error) {
		// Verify signing method
		if _, methodOK := token.Method.(*jwt.SigningMethodRSA); !methodOK {
			keyErr = fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			return key, keyErr
		}
		key = publicKey
		return key, keyErr
	})

	if err != nil {
		err = fmt.Errorf("token validation failed: %w", err)
		return result, err
	}

	if !validatedToken.Valid {
		err = errors.New("token is invalid")
		return result, err
	}

	// Extract claims
	claims, ok := validatedToken.Claims.(jwt.MapClaims)
	if !ok {
		err = errors.New("invalid token claims format")
		return result, err
	}

	// Validate standard claims
	err = a.validateStandardClaims(claims)
	if err != nil {
		err = fmt.Errorf("standard claims validation failed: %w", err)
		return result, err
	}

	// Extract user information
	result = &Result{
		Authenticated: true,
		Method:        a.Name(),
	}

	if sub, subOK := claims["sub"].(string); subOK {
		result.Subject = sub
	}
	if username, usernameOK := claims["preferred_username"].(string); usernameOK {
		result.Username = username
	}
	if email, emailOK := claims["email"].(string); emailOK {
		result.Email = email
	}

	// Extract groups - handle both []string and []interface{}
	if groupsRaw, groupsOK := claims["groups"]; groupsOK {
		switch groups := groupsRaw.(type) {
		case []string:
			result.Groups = groups
		case []interface{}:
			for _, g := range groups {
				if groupStr, groupStrOK := g.(string); groupStrOK {
					result.Groups = append(result.Groups, groupStr)
				}
			}
		}
	}

	// Validate authorization (group membership)
	err = a.validateAuthorization(result)
	if err != nil {
		err = fmt.Errorf("authorization failed: %w", err)
		return result, err
	}

	a.logger.InfoContext(r.Context(), "OIDC token validated successfully",
		slog.String("subject", result.Subject),
		slog.String("username", result.Username),
		slog.String("email", result.Email),
		slog.Any("groups", result.Groups))

	return result, err
}

// validateStandardClaims validates standard JWT claims.
func (a *OIDCAuth) validateStandardClaims(claims jwt.MapClaims) (err error) {
	// Validate issuer
	if !a.skipIssuerVerify {
		iss, ok := claims["iss"].(string)
		if !ok || iss != a.issuerURL {
			err = fmt.Errorf("invalid issuer: expected %s, got %s", a.issuerURL, iss)
			return err
		}
	}

	// Validate audience
	if a.audience != "" {
		aud, ok := claims["aud"].(string)
		if !ok || aud != a.audience {
			err = fmt.Errorf("invalid audience: expected %s, got %s", a.audience, aud)
			return err
		}
	}

	// Validate expiration
	if exp, ok := claims["exp"].(float64); ok {
		if time.Unix(int64(exp), 0).Before(time.Now()) {
			err = errors.New("token has expired")
			return err
		}
	} else {
		err = errors.New("missing exp claim")
		return err
	}

	// Validate not before if present
	if nbf, ok := claims["nbf"].(float64); ok {
		if time.Unix(int64(nbf), 0).After(time.Now()) {
			err = errors.New("token not yet valid")
			return err
		}
	}

	return err
}

// validateAuthorization checks if user is authorized based on group membership.
func (a *OIDCAuth) validateAuthorization(result *Result) (err error) {
	// If no groups configured, allow all authenticated users
	if len(a.allowedGroups) == 0 {
		return err
	}

	// Check if user is in any allowed group
	for _, userGroup := range result.Groups {
		for _, allowedGroup := range a.allowedGroups {
			if userGroup == allowedGroup {
				a.logger.Debug("User authorized via group membership",
					slog.String("username", result.Username),
					slog.String("group", userGroup))
				return err
			}
		}
	}

	err = fmt.Errorf("user %s not in any allowed groups %v, user groups: %v",
		result.Username, a.allowedGroups, result.Groups)
	return err
}

// getPublicKey retrieves and caches public keys from JWKS endpoint.
func (a *OIDCAuth) getPublicKey(kid string) (key *rsa.PublicKey, err error) {
	// Check cache first
	if time.Now().Before(a.jwksCacheExp) {
		var exists bool
		key, exists = a.jwksCache[kid]
		if exists {
			return key, err
		}
	}

	// Refresh cache
	err = a.refreshJWKSCache()
	if err != nil {
		err = fmt.Errorf("failed to refresh JWKS cache: %w", err)
		return key, err
	}

	// Try cache again
	var exists bool
	key, exists = a.jwksCache[kid]
	if exists {
		return key, err
	}

	err = fmt.Errorf("key with kid %s not found in JWKS", kid)
	return key, err
}

// refreshJWKSCache fetches and caches public keys from JWKS endpoint.
func (a *OIDCAuth) refreshJWKSCache() (err error) {
	jwksURL := strings.TrimSuffix(a.issuerURL, "/") + "/keys"

	// Per-client Transport isolates the connection pool from
	// http.DefaultTransport (see pkg/mcp/tempo.go for context).
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{},
	}
	var req *http.Request
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, jwksURL, nil)
	if err != nil {
		err = fmt.Errorf("failed to create JWKS request: %w", err)
		return err
	}

	var resp *http.Response
	resp, err = client.Do(req)
	if err != nil {
		err = fmt.Errorf("failed to fetch JWKS: %w", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
		return err
	}

	var jwksResp JWKSResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&jwksResp)
	if decodeErr != nil {
		err = fmt.Errorf("failed to decode JWKS response: %w", decodeErr)
		return err
	}

	// Clear existing cache
	a.jwksCache = make(map[string]*rsa.PublicKey)

	// Process each key
	for _, jwk := range jwksResp.Keys {
		if jwk.Kty != "RSA" {
			continue // Only support RSA keys for now
		}

		publicKey, parseErr := a.parseRSAPublicKey(&jwk)
		if parseErr != nil {
			a.logger.Warn("Failed to parse JWK", slog.String("kid", jwk.Kid), slog.Any("error", parseErr))
			continue
		}

		a.jwksCache[jwk.Kid] = publicKey
		a.logger.Debug("Cached public key", slog.String("kid", jwk.Kid))
	}

	// Set cache expiration
	a.jwksCacheExp = time.Now().Add(time.Duration(a.jwksCacheTime) * time.Second)

	a.logger.Info("JWKS cache refreshed",
		slog.Int("keys_cached", len(a.jwksCache)),
		slog.Time("expires_at", a.jwksCacheExp))

	return err
}

// parseRSAPublicKey converts a JWK to RSA public key.
func (a *OIDCAuth) parseRSAPublicKey(jwk *JWK) (key *rsa.PublicKey, err error) {
	// Decode N (modulus)
	var nBytes []byte
	nBytes, err = base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		err = fmt.Errorf("failed to decode modulus: %w", err)
		return key, err
	}

	// Decode E (exponent)
	var eBytes []byte
	eBytes, err = base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		err = fmt.Errorf("failed to decode exponent: %w", err)
		return key, err
	}

	// Convert to big.Int
	n := new(big.Int).SetBytes(nBytes)

	// Convert exponent bytes to int
	e := 0
	for _, b := range eBytes {
		e = e*256 + int(b)
	}

	key = &rsa.PublicKey{
		N: n,
		E: e,
	}
	return key, err
}
