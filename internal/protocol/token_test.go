package protocol

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestRSAParametersRoundtrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey failed: %v", err)
	}

	params := ExportRSAParameters(key)
	if params == nil {
		t.Fatalf("expected non-nil params")
	}
	if params.Modulus == "" || params.Exponent == "" || params.D == "" {
		t.Fatalf("exported parameters missing required fields: %+v", params)
	}
	if params.P == "" || params.Q == "" || params.DP == "" || params.DQ == "" || params.InverseQ == "" {
		t.Fatalf("exported parameters missing CRT fields: %+v", params)
	}

	parsedKey, err := ParseRSAPrivateKey(params)
	if err != nil {
		t.Fatalf("ParseRSAPrivateKey failed: %v", err)
	}

	if parsedKey.N.Cmp(key.N) != 0 || parsedKey.D.Cmp(key.D) != 0 {
		t.Fatalf("parsed key does not match original key")
	}
	if parsedKey.E != key.E {
		t.Fatalf("expected E %d, got %d", key.E, parsedKey.E)
	}
	if len(parsedKey.Primes) != 2 || parsedKey.Primes[0].Cmp(key.Primes[0]) != 0 || parsedKey.Primes[1].Cmp(key.Primes[1]) != 0 {
		t.Fatalf("parsed primes do not match original primes")
	}
	if parsedKey.Precomputed.Dp.Cmp(key.Precomputed.Dp) != 0 || parsedKey.Precomputed.Dq.Cmp(key.Precomputed.Dq) != 0 {
		t.Fatalf("parsed precomputed CRT values do not match original")
	}
}

func TestExportRSAParameters_Nil(t *testing.T) {
	if ExportRSAParameters(nil) != nil {
		t.Fatalf("expected nil when exporting nil key")
	}
}

func TestParseRSAPrivateKey_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		params *RSAParameters
	}{
		{"nil params", nil},
		{"missing modulus", &RSAParameters{Exponent: "AQAB", D: "c29tZXRoaW5n"}},
		{"missing exponent", &RSAParameters{Modulus: "c29tZXRoaW5n", D: "c29tZXRoaW5n"}},
		{"missing d", &RSAParameters{Modulus: "c29tZXRoaW5n", Exponent: "AQAB"}},
		{"invalid modulus base64", &RSAParameters{Modulus: "!!invalid!!", Exponent: "AQAB", D: "c29tZXRoaW5n"}},
		{"invalid exponent base64", &RSAParameters{Modulus: "c29tZXRoaW5n", Exponent: "!!invalid!!", D: "c29tZXRoaW5n"}},
		{"invalid d base64", &RSAParameters{Modulus: "c29tZXRoaW5n", Exponent: "AQAB", D: "!!invalid!!"}},
		{"invalid p base64", &RSAParameters{Modulus: "c29tZXRoaW5n", Exponent: "AQAB", D: "c29tZXRoaW5n", P: "!!invalid!!"}},
		{"invalid q base64", &RSAParameters{Modulus: "c29tZXRoaW5n", Exponent: "AQAB", D: "c29tZXRoaW5n", P: "c29tZXRoaW5n", Q: "!!invalid!!"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRSAPrivateKey(tt.params)
			if err == nil {
				t.Fatalf("expected error for case %q, got nil", tt.name)
			}
		})
	}
}

func TestAcquireOAuthToken_Success(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	var capturedAssertion string
	var capturedGrantType string
	var capturedAssertionType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "invalid method", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		capturedGrantType = r.FormValue("grant_type")
		if capturedGrantType != "client_credentials" {
			http.Error(w, "invalid grant_type", http.StatusBadRequest)
			return
		}
		capturedAssertion = r.FormValue("client_assertion")
		if capturedAssertion == "" {
			http.Error(w, "missing assertion", http.StatusBadRequest)
			return
		}
		capturedAssertionType = r.FormValue("client_assertion_type")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mock-access-token-12345",
			"expires_in":   3600,
			"token_type":   "bearer",
		})
	}))
	defer server.Close()

	clientID := "mock-client-id"
	token, err := AcquireOAuthToken(context.Background(), server.Client(), server.URL, clientID, key)
	if err != nil {
		t.Fatalf("AcquireOAuthToken failed: %v", err)
	}
	if token != "mock-access-token-12345" {
		t.Errorf("expected token 'mock-access-token-12345', got %q", token)
	}

	if capturedGrantType != "client_credentials" {
		t.Errorf("expected grant_type client_credentials, got %s", capturedGrantType)
	}
	if capturedAssertionType != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		t.Errorf("unexpected client_assertion_type: %s", capturedAssertionType)
	}

	// Verify the JWT signature and claims using the public key
	parsedToken, err := jwt.Parse(capturedAssertion, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSAPSS); !ok {
			t.Fatalf("unexpected signing method: %v", tok.Header["alg"])
		}
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("failed to parse/verify client_assertion JWT: %v", err)
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok || !parsedToken.Valid {
		t.Fatalf("invalid claims in client_assertion token")
	}

	if claims["iss"] != clientID {
		t.Errorf("expected iss %s, got %v", clientID, claims["iss"])
	}
	if claims["sub"] != clientID {
		t.Errorf("expected sub %s, got %v", clientID, claims["sub"])
	}
	if claims["aud"] != server.URL {
		// Could be string or []any
		switch aud := claims["aud"].(type) {
		case string:
			if aud != server.URL {
				t.Errorf("expected aud %s, got %s", server.URL, aud)
			}
		case []any:
			if len(aud) == 0 || aud[0] != server.URL {
				t.Errorf("expected aud %s, got %v", server.URL, aud)
			}
		default:
			t.Errorf("unexpected aud type: %T %v", claims["aud"], claims["aud"])
		}
	}
}

func TestAcquireOAuthToken_Errors(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	t.Run("nil key", func(t *testing.T) {
		_, err := AcquireOAuthToken(context.Background(), http.DefaultClient, "http://localhost", "client", nil)
		if err == nil {
			t.Fatal("expected error with nil key")
		}
	})

	t.Run("empty authURL", func(t *testing.T) {
		_, err := AcquireOAuthToken(context.Background(), http.DefaultClient, "", "client", key)
		if err == nil {
			t.Fatal("expected error with empty authURL")
		}
	})

	t.Run("empty clientID", func(t *testing.T) {
		_, err := AcquireOAuthToken(context.Background(), http.DefaultClient, "http://localhost", "", key)
		if err == nil {
			t.Fatal("expected error with empty clientID")
		}
	})

	t.Run("server returns 400 bad request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
		}))
		defer server.Close()

		_, err := AcquireOAuthToken(context.Background(), server.Client(), server.URL, "client", key)
		if err == nil {
			t.Fatal("expected error for 400 status")
		}
	})

	t.Run("server returns invalid JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not valid json"))
		}))
		defer server.Close()

		_, err := AcquireOAuthToken(context.Background(), server.Client(), server.URL, "client", key)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("server returns empty access token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": ""})
		}))
		defer server.Close()

		_, err := AcquireOAuthToken(context.Background(), server.Client(), server.URL, "client", key)
		if err == nil {
			t.Fatal("expected error for empty access token")
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
		}))
		defer server.Close()

		_, err := AcquireOAuthToken(ctx, server.Client(), server.URL, "client", key)
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})
}

func TestParseRSAPrivateKey_RejectsInvalidPrimesWithoutPanic(t *testing.T) {
	_, err := ParseRSAPrivateKey(&RSAParameters{
		Modulus:  base64.RawURLEncoding.EncodeToString([]byte{0x0f}),
		Exponent: base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01}),
		D:        base64.RawURLEncoding.EncodeToString([]byte{0x03}),
		P:        base64.RawURLEncoding.EncodeToString([]byte{0x01}), // invalid prime == 1
		Q:        base64.RawURLEncoding.EncodeToString([]byte{0x01}),
	})
	require.Error(t, err)
}

func TestParseRSAPrivateKey_ValidationChecks(t *testing.T) {
	validExp := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01})
	// Exponent = 2 (even)
	_, err := ParseRSAPrivateKey(&RSAParameters{
		Modulus:  base64.RawURLEncoding.EncodeToString([]byte{0x0f}),
		Exponent: base64.RawURLEncoding.EncodeToString([]byte{0x02}),
		D:        base64.RawURLEncoding.EncodeToString([]byte{0x03}),
	})
	require.Error(t, err)

	// Exponent > int64
	_, err = ParseRSAPrivateKey(&RSAParameters{
		Modulus:  base64.RawURLEncoding.EncodeToString([]byte{0x0f}),
		Exponent: base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}),
		D:        base64.RawURLEncoding.EncodeToString([]byte{0x03}),
	})
	require.Error(t, err)

	// Zero modulus
	_, err = ParseRSAPrivateKey(&RSAParameters{
		Modulus:  base64.RawURLEncoding.EncodeToString([]byte{0x00}),
		Exponent: validExp,
		D:        base64.RawURLEncoding.EncodeToString([]byte{0x03}),
	})
	require.Error(t, err)

	// Zero D
	_, err = ParseRSAPrivateKey(&RSAParameters{
		Modulus:  base64.RawURLEncoding.EncodeToString([]byte{0x0f}),
		Exponent: validExp,
		D:        base64.RawURLEncoding.EncodeToString([]byte{0x00}),
	})
	require.Error(t, err)
}

func TestAcquireOAuthToken_RejectsInsecureRemoteURL(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	_, err = AcquireOAuthToken(context.Background(), nil, "http://auth.example.com/token", "cid", key)
	require.Error(t, err)
}
