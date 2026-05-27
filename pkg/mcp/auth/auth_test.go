package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/require"
)

func TestStaticTokenAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		configToken    string
		authHeader     string
		wantAuth       bool
		wantErrMessage string
	}{
		{
			name:           "missing header",
			configToken:    "secret",
			authHeader:     "",
			wantAuth:       false,
			wantErrMessage: "missing Authorization header",
		},
		{
			name:           "invalid format",
			configToken:    "secret",
			authHeader:     "InvalidFormat token",
			wantAuth:       false,
			wantErrMessage: "invalid Authorization header format",
		},
		{
			name:           "wrong token",
			configToken:    "secret",
			authHeader:     "Bearer wrong",
			wantAuth:       false,
			wantErrMessage: "invalid token",
		},
		{
			name:        "correct token",
			configToken: "secret-token-123",
			authHeader:  "Bearer secret-token-123",
			wantAuth:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := NewStaticTokenAuth(tt.configToken)
			require.Equal(t, "static-bearer", auth.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			result, err := auth.Authenticate(req)

			if tt.wantAuth {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Authenticated)
				require.Equal(t, "static-bearer", result.Method)
				require.Equal(t, "static-token-user", result.Username)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			}
		})
	}
}

func TestJWTAuth(t *testing.T) {
	t.Parallel()

	secret := []byte("test-secret")

	tests := []struct {
		name           string
		algorithm      string
		buildToken     func() string
		wantAuth       bool
		wantErrMessage string
	}{
		{
			name:      "valid HS256 token",
			algorithm: "HS256",
			buildToken: func() (tokenString string) {
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"sub":                "user123",
					"preferred_username": "testuser",
					"email":              "test@example.com",
					"groups":             []string{"admin", "users"},
					"exp":                time.Now().Add(time.Hour).Unix(),
				})
				tokenString, _ = token.SignedString(secret)
				return tokenString
			},
			wantAuth: true,
		},
		{
			name:      "expired token",
			algorithm: "HS256",
			buildToken: func() (tokenString string) {
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"sub": "user123",
					"exp": time.Now().Add(-time.Hour).Unix(),
				})
				tokenString, _ = token.SignedString(secret)
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "expired",
		},
		{
			name:      "invalid signature",
			algorithm: "HS256",
			buildToken: func() (tokenString string) {
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"sub": "user123",
					"exp": time.Now().Add(time.Hour).Unix(),
				})
				tokenString, _ = token.SignedString([]byte("wrong-secret"))
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jwtAuth, err := NewJWTAuth(&JWTConfig{
				Secret:    secret,
				Algorithm: tt.algorithm,
			})
			require.NoError(t, err)
			require.Equal(t, "jwt", jwtAuth.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			tokenString := tt.buildToken()
			req.Header.Set("Authorization", "Bearer "+tokenString)

			result, err := jwtAuth.Authenticate(req)

			if tt.wantAuth {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Authenticated)
				require.Equal(t, "jwt", result.Method)
				require.NotEmpty(t, result.Subject)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			}
		})
	}
}

func TestAPIKeyAuth(t *testing.T) {
	t.Parallel()

	keys := map[string]string{
		"key1": "user1",
		"key2": "user2",
		"key3": "admin",
	}

	tests := []struct {
		name           string
		apiKey         string
		wantAuth       bool
		wantUsername   string
		wantErrMessage string
	}{
		{
			name:           "missing header",
			apiKey:         "",
			wantAuth:       false,
			wantErrMessage: "missing X-API-Key header",
		},
		{
			name:           "invalid key",
			apiKey:         "invalid-key",
			wantAuth:       false,
			wantErrMessage: "invalid API key",
		},
		{
			name:         "valid key1",
			apiKey:       "key1",
			wantAuth:     true,
			wantUsername: "user1",
		},
		{
			name:         "valid key2",
			apiKey:       "key2",
			wantAuth:     true,
			wantUsername: "user2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := NewAPIKeyAuth(keys)
			require.Equal(t, "api-key", auth.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.apiKey != "" {
				req.Header.Set("X-Api-Key", tt.apiKey)
			}

			result, err := auth.Authenticate(req)

			if tt.wantAuth {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Authenticated)
				require.Equal(t, "api-key", result.Method)
				require.Equal(t, tt.wantUsername, result.Username)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			}
		})
	}
}

func TestChain(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	tests := []struct {
		name           string
		methods        []Method
		authHeader     string
		apiKeyHeader   string
		wantAuth       bool
		wantMethod     string
		wantErrMessage string
	}{
		{
			name:       "no methods - allows all",
			methods:    []Method{},
			wantAuth:   true,
			wantMethod: "none",
		},
		{
			name: "first method succeeds",
			methods: []Method{
				NewStaticTokenAuth("token1"),
				NewAPIKeyAuth(map[string]string{"key1": "user1"}),
			},
			authHeader: "Bearer token1",
			wantAuth:   true,
			wantMethod: "static-bearer",
		},
		{
			name: "first fails, second succeeds",
			methods: []Method{
				NewStaticTokenAuth("token1"),
				NewAPIKeyAuth(map[string]string{"key1": "user1"}),
			},
			authHeader:   "Bearer wrong-token",
			apiKeyHeader: "key1",
			wantAuth:     true,
			wantMethod:   "api-key",
		},
		{
			name: "all methods fail",
			methods: []Method{
				NewStaticTokenAuth("token1"),
				NewAPIKeyAuth(map[string]string{"key1": "user1"}),
			},
			authHeader:     "Bearer wrong-token",
			apiKeyHeader:   "wrong-key",
			wantAuth:       false,
			wantErrMessage: "all authentication methods failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain := NewChain(tt.methods, logger)
			require.Equal(t, "auth-chain", chain.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.apiKeyHeader != "" {
				req.Header.Set("X-Api-Key", tt.apiKeyHeader)
			}

			result, err := chain.Authenticate(req)

			if tt.wantAuth {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Authenticated)
				require.Equal(t, tt.wantMethod, result.Method)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			}
		})
	}
}

func TestMTLSAuth(t *testing.T) {
	// Don't run in parallel because we need to coordinate temp file cleanup

	// Generate a test CA certificate
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Test CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(caCertDER)
	require.NoError(t, err)

	// Write CA cert to temp file
	tempDir := t.TempDir()
	caCertPath := tempDir + "/ca-cert.pem"
	caCertFile, err := os.Create(caCertPath)
	require.NoError(t, err)

	err = pem.Encode(caCertFile, &pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	require.NoError(t, err)
	caCertFile.Close()

	// Generate a client certificate
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "testuser",
		},
		EmailAddresses: []string{"test@example.com"},
		NotBefore:      time.Now(),
		NotAfter:       time.Now().Add(24 * time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	require.NoError(t, err)

	clientCert, err := x509.ParseCertificate(clientCertDER)
	require.NoError(t, err)

	tests := []struct {
		name           string
		verifyClient   bool
		provideCert    bool
		wantAuth       bool
		wantErrMessage string
	}{
		{
			name:           "no TLS connection",
			verifyClient:   true,
			provideCert:    false,
			wantAuth:       false,
			wantErrMessage: "no TLS connection",
		},
		{
			name:         "valid cert with verification",
			verifyClient: true,
			provideCert:  true,
			wantAuth:     true,
		},
		{
			name:         "valid cert without verification",
			verifyClient: false,
			provideCert:  true,
			wantAuth:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Don't run subtests in parallel either

			mtlsAuth, mtlsErr := NewMTLSAuth(&MTLSConfig{
				CACertPath:   caCertPath,
				VerifyClient: tt.verifyClient,
			})
			require.NoError(t, mtlsErr)
			require.Equal(t, "mtls", mtlsAuth.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			if tt.provideCert {
				// Mock TLS connection state
				req.TLS = &tls.ConnectionState{
					PeerCertificates: []*x509.Certificate{clientCert},
				}
			}

			result, authErr := mtlsAuth.Authenticate(req)

			if tt.wantAuth {
				require.NoError(t, authErr)
				require.NotNil(t, result)
				require.True(t, result.Authenticated)
				require.Equal(t, "mtls", result.Method)
				require.Equal(t, "testuser", result.Username)
				require.Equal(t, "test@example.com", result.Email)
			} else {
				require.Error(t, authErr)
				require.Nil(t, result)
				require.Contains(t, authErr.Error(), tt.wantErrMessage)
			}
		})
	}
}

// setupMockJWKSServer creates a mock OIDC server that serves JWKS at /keys.
// Follows the pattern from dex/connector/oidc/oidc_test.go.
func setupMockJWKSServer(t *testing.T, key *rsa.PrivateKey) (server *httptest.Server) {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encodeErr := json.NewEncoder(w).Encode(&map[string]interface{}{
			"keys": []map[string]interface{}{{
				"kty": "RSA",
				"use": "sig",
				"kid": "test-key-id",
				"n":   encodeRSAModulus(&key.PublicKey),
				"e":   encodeRSAExponent(&key.PublicKey),
			}},
		})
		if encodeErr != nil {
			t.Errorf("failed to encode JWKS response: %v", encodeErr)
		}
	})

	server = httptest.NewServer(mux)
	return server
}

// signOIDCToken creates an RSA-signed JWT with the given claims and kid header.
func signOIDCToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) (tokenString string) {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	var err error
	tokenString, err = token.SignedString(key)
	require.NoError(t, err)

	return tokenString
}

// encodeRSAModulus returns the base64url-encoded modulus of an RSA public key.
func encodeRSAModulus(pub *rsa.PublicKey) (encoded string) {
	encoded = base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	return encoded
}

// encodeRSAExponent returns the base64url-encoded exponent of an RSA public key.
func encodeRSAExponent(pub *rsa.PublicKey) (encoded string) {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(pub.E))
	encoded = base64.RawURLEncoding.EncodeToString(bytes.TrimLeft(data, "\x00"))
	return encoded
}

func TestOIDCAuth(t *testing.T) {
	t.Parallel()

	// Generate RSA key pair for signing tokens
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Set up mock JWKS server - use t.Cleanup so server stays alive for parallel subtests
	server := setupMockJWKSServer(t, rsaKey)
	t.Cleanup(server.Close)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	tests := []struct {
		name           string
		config         *OIDCConfig
		buildToken     func() string
		wantAuth       bool
		wantSubject    string
		wantUsername   string
		wantEmail      string
		wantGroups     []string
		wantErrMessage string
	}{
		{
			name: "valid token with all claims",
			config: &OIDCConfig{
				IssuerURL:     server.URL,
				Audience:      "test-audience",
				AllowedGroups: []string{"platform-team", "sre"},
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss":                server.URL,
					"aud":                "test-audience",
					"sub":                "user-12345",
					"preferred_username": "jdoe",
					"email":              "jdoe@example.com",
					"groups":             []interface{}{"platform-team", "developers"},
					"exp":                float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:     true,
			wantSubject:  "user-12345",
			wantUsername: "jdoe",
			wantEmail:    "jdoe@example.com",
			wantGroups:   []string{"platform-team", "developers"},
		},
		{
			name: "valid token with no audience configured",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss":                server.URL,
					"sub":                "user-99",
					"preferred_username": "admin",
					"email":              "admin@example.com",
					"exp":                float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:     true,
			wantSubject:  "user-99",
			wantUsername: "admin",
			wantEmail:    "admin@example.com",
		},
		{
			name: "valid token with no groups required",
			config: &OIDCConfig{
				IssuerURL:     server.URL,
				AllowedGroups: []string{}, // no group restriction
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-ngroups",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:    true,
			wantSubject: "user-ngroups",
		},
		{
			name: "valid token with skip issuer verify",
			config: &OIDCConfig{
				IssuerURL:        server.URL,
				SkipIssuerVerify: true,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": "https://some-other-issuer.example.com",
					"sub": "user-skip-iss",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:    true,
			wantSubject: "user-skip-iss",
		},
		{
			name: "expired token",
			config: &OIDCConfig{
				IssuerURL:        server.URL,
				SkipIssuerVerify: true,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-expired",
					"exp": float64(time.Now().Add(-time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "expired",
		},
		{
			name: "wrong issuer",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": "https://wrong-issuer.example.com",
					"sub": "user-wrong-iss",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "invalid issuer",
		},
		{
			name: "wrong audience",
			config: &OIDCConfig{
				IssuerURL: server.URL,
				Audience:  "expected-audience",
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"aud": "wrong-audience",
					"sub": "user-wrong-aud",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "invalid audience",
		},
		{
			name: "user not in allowed groups",
			config: &OIDCConfig{
				IssuerURL:     server.URL,
				AllowedGroups: []string{"admin", "sre"},
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss":                server.URL,
					"sub":                "user-nogroup",
					"preferred_username": "nogroup-user",
					"groups":             []interface{}{"developers", "readonly"},
					"exp":                float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "not in any allowed groups",
		},
		{
			name: "token not yet valid - nbf in future",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-nbf",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
					"nbf": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "not valid yet",
		},
		{
			name: "unknown kid - key not in JWKS",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "unknown-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-unknown-kid",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "not found in JWKS",
		},
		{
			name: "missing exp claim",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				tokenString = signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-no-exp",
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "missing exp claim",
		},
		{
			name: "token signed with wrong key",
			config: &OIDCConfig{
				IssuerURL: server.URL,
			},
			buildToken: func() (tokenString string) {
				// Generate a different RSA key
				wrongKey, genErr := rsa.GenerateKey(rand.Reader, 2048)
				require.NoError(t, genErr)

				tokenString = signOIDCToken(t, wrongKey, "test-key-id", jwt.MapClaims{
					"iss": server.URL,
					"sub": "user-wrong-key",
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				return tokenString
			},
			wantAuth:       false,
			wantErrMessage: "token validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			oidcAuth := NewOIDCAuth(tt.config, logger)
			require.Equal(t, "oidc", oidcAuth.Name())

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			tokenString := tt.buildToken()
			req.Header.Set("Authorization", "Bearer "+tokenString)

			result, authErr := oidcAuth.Authenticate(req)

			if !tt.wantAuth {
				require.Error(t, authErr)
				require.Contains(t, authErr.Error(), tt.wantErrMessage)
				return
			}

			require.NoError(t, authErr)
			require.NotNil(t, result)
			require.True(t, result.Authenticated)
			require.Equal(t, "oidc", result.Method)
			verifyOIDCResult(t, result, tt.wantSubject, tt.wantUsername, tt.wantEmail, tt.wantGroups)
		})
	}
}

func verifyOIDCResult(t *testing.T, result *Result, wantSubject, wantUsername, wantEmail string, wantGroups []string) {
	t.Helper()

	if wantSubject != "" {
		require.Equal(t, wantSubject, result.Subject)
	}
	if wantUsername != "" {
		require.Equal(t, wantUsername, result.Username)
	}
	if wantEmail != "" {
		require.Equal(t, wantEmail, result.Email)
	}
	if wantGroups != nil {
		require.Equal(t, wantGroups, result.Groups)
	}
}

func TestOIDCAuth_NoAuthHeader(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: "https://issuer.example.com",
	}, logger)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	result, err := oidcAuth.Authenticate(req)

	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "missing Authorization header")
}

func TestOIDCAuth_MissingKid(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create a token WITHOUT kid in the header
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-no-kid",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	// Explicitly don't set kid header

	tokenString, err := token.SignedString(rsaKey)
	require.NoError(t, err)

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: "https://issuer.example.com",
	}, logger)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.Error(t, authErr)
	require.Nil(t, result)
	require.Contains(t, authErr.Error(), "missing kid")
}

func TestOIDCAuth_JWKSServerDown(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create a server and immediately close it
	server := httptest.NewServer(http.NewServeMux())
	serverURL := server.URL
	server.Close()

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: serverURL,
	}, logger)

	tokenString := signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
		"iss": serverURL,
		"sub": "user-server-down",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.Error(t, authErr)
	require.Nil(t, result)
	require.Contains(t, authErr.Error(), "failed to get public key")
}

func TestOIDCAuth_JWKSServerBadStatus(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Server that returns 500 on /keys
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: server.URL,
	}, logger)

	tokenString := signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-bad-status",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.Error(t, authErr)
	require.Nil(t, result)
	require.Contains(t, authErr.Error(), "status 500")
}

func TestOIDCAuth_JWKSServerBadJSON(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Server that returns invalid JSON on /keys
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "not valid json{{{")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: server.URL,
	}, logger)

	tokenString := signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-bad-json",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.Error(t, authErr)
	require.Nil(t, result)
	require.Contains(t, authErr.Error(), "failed to get public key")
}

func TestOIDCAuth_JWKSCaching(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&map[string]interface{}{
			"keys": []map[string]interface{}{{
				"kty": "RSA",
				"use": "sig",
				"kid": "test-key-id",
				"n":   encodeRSAModulus(&rsaKey.PublicKey),
				"e":   encodeRSAExponent(&rsaKey.PublicKey),
			}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL:     server.URL,
		JWKSCacheTime: 300, // 5 minutes
	}, logger)

	// First request should fetch JWKS
	tokenString := signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-cache-1",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.NoError(t, authErr)
	require.NotNil(t, result)
	require.Equal(t, 1, callCount)

	// Second request should use cached key
	tokenString2 := signOIDCToken(t, rsaKey, "test-key-id", jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-cache-2",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("Authorization", "Bearer "+tokenString2)

	result2, authErr2 := oidcAuth.Authenticate(req2)
	require.NoError(t, authErr2)
	require.NotNil(t, result2)
	require.Equal(t, "user-cache-2", result2.Subject)
	require.Equal(t, 1, callCount, "JWKS should have been cached, not fetched again")
}

func TestOIDCAuth_NonRSASigningMethod(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := setupMockJWKSServer(t, rsaKey)
	defer server.Close()

	// Create an HMAC-signed token but with a kid that matches the JWKS
	hmacSecret := []byte("test-hmac-secret")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-hmac",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	token.Header["kid"] = "test-key-id"

	tokenString, err := token.SignedString(hmacSecret)
	require.NoError(t, err)

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: server.URL,
	}, logger)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	result, authErr := oidcAuth.Authenticate(req)
	require.Error(t, authErr)
	require.Nil(t, result)
	require.Contains(t, authErr.Error(), "unexpected signing method")
}

func TestValidateStandardClaims(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	tests := []struct {
		name             string
		issuerURL        string
		audience         string
		skipIssuerVerify bool
		claims           jwt.MapClaims
		wantErrMessage   string
	}{
		{
			name:      "valid claims",
			issuerURL: "https://issuer.example.com",
			audience:  "my-audience",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
				"aud": "my-audience",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
			},
		},
		{
			name:      "wrong issuer",
			issuerURL: "https://issuer.example.com",
			claims: jwt.MapClaims{
				"iss": "https://wrong.example.com",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
			},
			wantErrMessage: "invalid issuer",
		},
		{
			name:             "skip issuer verify",
			issuerURL:        "https://issuer.example.com",
			skipIssuerVerify: true,
			claims: jwt.MapClaims{
				"iss": "https://totally-different.example.com",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
			},
		},
		{
			name:      "wrong audience",
			issuerURL: "https://issuer.example.com",
			audience:  "expected",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
				"aud": "wrong",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
			},
			wantErrMessage: "invalid audience",
		},
		{
			name:      "expired",
			issuerURL: "https://issuer.example.com",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
				"exp": float64(time.Now().Add(-time.Hour).Unix()),
			},
			wantErrMessage: "token has expired",
		},
		{
			name:      "missing exp",
			issuerURL: "https://issuer.example.com",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
			},
			wantErrMessage: "missing exp claim",
		},
		{
			name:      "not yet valid - nbf in future",
			issuerURL: "https://issuer.example.com",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
				"nbf": float64(time.Now().Add(time.Hour).Unix()),
			},
			wantErrMessage: "token not yet valid",
		},
		{
			name:      "no audience configured - any audience accepted",
			issuerURL: "https://issuer.example.com",
			audience:  "",
			claims: jwt.MapClaims{
				"iss": "https://issuer.example.com",
				"aud": "any-audience",
				"exp": float64(time.Now().Add(time.Hour).Unix()),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			oidcAuth := NewOIDCAuth(&OIDCConfig{
				IssuerURL:        tt.issuerURL,
				Audience:         tt.audience,
				SkipIssuerVerify: tt.skipIssuerVerify,
			}, logger)

			err := oidcAuth.validateStandardClaims(tt.claims)
			if tt.wantErrMessage != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateAuthorization(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	tests := []struct {
		name           string
		allowedGroups  []string
		userGroups     []string
		wantErr        bool
		wantErrMessage string
	}{
		{
			name:          "no groups configured - allow all",
			allowedGroups: []string{},
			userGroups:    []string{"anything"},
			wantErr:       false,
		},
		{
			name:          "user in allowed group",
			allowedGroups: []string{"admin", "sre"},
			userGroups:    []string{"developers", "sre"},
			wantErr:       false,
		},
		{
			name:           "user not in any allowed group",
			allowedGroups:  []string{"admin", "sre"},
			userGroups:     []string{"developers", "readonly"},
			wantErr:        true,
			wantErrMessage: "not in any allowed groups",
		},
		{
			name:           "user has no groups",
			allowedGroups:  []string{"admin"},
			userGroups:     []string{},
			wantErr:        true,
			wantErrMessage: "not in any allowed groups",
		},
		{
			name:          "nil allowed groups - allow all",
			allowedGroups: nil,
			userGroups:    []string{"anything"},
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			oidcAuth := NewOIDCAuth(&OIDCConfig{
				IssuerURL:     "https://issuer.example.com",
				AllowedGroups: tt.allowedGroups,
			}, logger)

			result := &Result{
				Username: "testuser",
				Groups:   tt.userGroups,
			}

			err := oidcAuth.validateAuthorization(result)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestOIDCAuthorizationHostedDomainFilter is the regression guard for the
// hosted-domain authz layer. Authentication is Google's job (signed
// claims); the bot's job is "given the claim, is this person allowed?"
// We don't depend on Google's `hd` claim because Dex doesn't pass it
// through — we derive the domain from the email's `@` suffix.
func TestOIDCAuthorizationHostedDomainFilter(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name           string
		allowed        []string
		email          string
		wantErr        bool
		wantErrMessage string
	}{
		{
			name:    "empty allowlist allows any domain",
			allowed: nil,
			email:   "alice@example.com",
			wantErr: false,
		},
		{
			name:    "email domain matches allowed",
			allowed: []string{"katn-solutions.io"},
			email:   "alice@katn-solutions.io",
			wantErr: false,
		},
		{
			name:           "email domain not in allowlist",
			allowed:        []string{"katn-solutions.io"},
			email:          "mallory@evil.example",
			wantErr:        true,
			wantErrMessage: "hosted domain",
		},
		{
			name:           "missing email when allowlist set",
			allowed:        []string{"katn-solutions.io"},
			email:          "",
			wantErr:        true,
			wantErrMessage: "hosted domain",
		},
		{
			name:           "malformed email without @ when allowlist set",
			allowed:        []string{"katn-solutions.io"},
			email:          "not-an-email",
			wantErr:        true,
			wantErrMessage: "hosted domain",
		},
		{
			name:    "multiple allowed domains, one matches",
			allowed: []string{"katn-solutions.io", "example.com"},
			email:   "bob@example.com",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oidcAuth := NewOIDCAuth(&OIDCConfig{
				IssuerURL:            "https://issuer.example.com",
				AllowedHostedDomains: tt.allowed,
			}, logger)
			err := oidcAuth.validateAuthorization(&Result{Username: "test", Email: tt.email})
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestOIDCAuthorizationEmailFilter verifies the per-user allowlist layer.
// Often combined with the domain filter ("anyone in this Workspace, AND
// only these specific addresses can write to Grafana").
func TestOIDCAuthorizationEmailFilter(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name           string
		allowed        []string
		email          string
		wantErr        bool
		wantErrMessage string
	}{
		{
			name:    "empty allowlist allows any email",
			allowed: nil,
			email:   "alice@example.com",
			wantErr: false,
		},
		{
			name:    "exact email match",
			allowed: []string{"alice@katn-solutions.io", "bob@katn-solutions.io"},
			email:   "alice@katn-solutions.io",
			wantErr: false,
		},
		{
			name:           "email not in allowlist",
			allowed:        []string{"alice@katn-solutions.io"},
			email:          "carol@katn-solutions.io",
			wantErr:        true,
			wantErrMessage: "email",
		},
		{
			name:           "missing email when allowlist set",
			allowed:        []string{"alice@katn-solutions.io"},
			email:          "",
			wantErr:        true,
			wantErrMessage: "email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oidcAuth := NewOIDCAuth(&OIDCConfig{
				IssuerURL:     "https://issuer.example.com",
				AllowedEmails: tt.allowed,
			}, logger)
			err := oidcAuth.validateAuthorization(&Result{Username: "test", Email: tt.email})
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMessage)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestOIDCAuthorizationCombinedFilters verifies both axes are enforced
// independently when configured together — failing either rejects.
func TestOIDCAuthorizationCombinedFilters(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL:            "https://issuer.example.com",
		AllowedHostedDomains: []string{"katn-solutions.io"},
		AllowedEmails:        []string{"alice@katn-solutions.io"},
		AllowedGroups:        []string{"sre"},
	}, logger)

	// Right domain, right email, right group → ok.
	err := oidcAuth.validateAuthorization(&Result{Username: "alice", Email: "alice@katn-solutions.io", Groups: []string{"sre"}})
	require.NoError(t, err)

	// Right domain, right group, wrong email → rejected by email filter.
	err = oidcAuth.validateAuthorization(&Result{Username: "alice", Email: "carol@katn-solutions.io", Groups: []string{"sre"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "email")

	// Right email + group, wrong domain → rejected by domain filter (domain runs first).
	err = oidcAuth.validateAuthorization(&Result{Username: "alice", Email: "alice@evil.example", Groups: []string{"sre"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hosted domain")

	// Right domain + email, missing group → rejected by group filter.
	err = oidcAuth.validateAuthorization(&Result{Username: "alice", Email: "alice@katn-solutions.io", Groups: []string{"developers"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "groups")
}

func TestParseRSAPublicKey(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	oidcAuth := NewOIDCAuth(&OIDCConfig{
		IssuerURL: "https://issuer.example.com",
	}, logger)

	t.Run("valid JWK", func(t *testing.T) {
		t.Parallel()

		jwk := &JWK{
			Kty: "RSA",
			Kid: "valid-key",
			N:   encodeRSAModulus(&rsaKey.PublicKey),
			E:   encodeRSAExponent(&rsaKey.PublicKey),
		}

		parsedKey, parseErr := oidcAuth.parseRSAPublicKey(jwk)
		require.NoError(t, parseErr)
		require.NotNil(t, parsedKey)
		require.Equal(t, 0, rsaKey.PublicKey.N.Cmp(parsedKey.N))
		require.Equal(t, rsaKey.PublicKey.E, parsedKey.E)
	})

	t.Run("invalid modulus", func(t *testing.T) {
		t.Parallel()

		jwk := &JWK{
			Kty: "RSA",
			Kid: "bad-n",
			N:   "!!!not-base64!!!",
			E:   encodeRSAExponent(&rsaKey.PublicKey),
		}

		parsedKey, parseErr := oidcAuth.parseRSAPublicKey(jwk)
		require.Error(t, parseErr)
		require.Nil(t, parsedKey)
		require.Contains(t, parseErr.Error(), "failed to decode modulus")
	})

	t.Run("invalid exponent", func(t *testing.T) {
		t.Parallel()

		jwk := &JWK{
			Kty: "RSA",
			Kid: "bad-e",
			N:   encodeRSAModulus(&rsaKey.PublicKey),
			E:   "!!!not-base64!!!",
		}

		parsedKey, parseErr := oidcAuth.parseRSAPublicKey(jwk)
		require.Error(t, parseErr)
		require.Nil(t, parsedKey)
		require.Contains(t, parseErr.Error(), "failed to decode exponent")
	})
}

func TestExtractBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		authHeader     string
		wantToken      string
		wantErrMessage string
	}{
		{
			name:           "missing header",
			authHeader:     "",
			wantErrMessage: "missing Authorization header",
		},
		{
			name:           "invalid format",
			authHeader:     "InvalidFormat token",
			wantErrMessage: "invalid Authorization header format",
		},
		{
			name:       "valid bearer token",
			authHeader: "Bearer my-token-123",
			wantToken:  "my-token-123",
		},
		{
			name:       "bearer with spaces",
			authHeader: "Bearer    token-with-spaces   ",
			wantToken:  "token-with-spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			token, err := extractBearerToken(req)

			if tt.wantErrMessage != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrMessage)
				require.Empty(t, token)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantToken, token)
			}
		})
	}
}
