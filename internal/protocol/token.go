package protocol

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// RSAParameters represents the .NET RSAParameters structure stored in .credentials_rsaparams.
// All fields are base64-encoded big-endian integer values.
type RSAParameters struct {
	D        string `json:"d"`
	DP       string `json:"dp"`
	DQ       string `json:"dq"`
	Exponent string `json:"exponent"`
	InverseQ string `json:"inverseQ"`
	Modulus  string `json:"modulus"`
	P        string `json:"p"`
	Q        string `json:"q"`
}

func decodeBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// ParseRSAPrivateKey parses .NET RSAParameters into a Go crypto/rsa PrivateKey.
func ParseRSAPrivateKey(params *RSAParameters) (*rsa.PrivateKey, error) {
	if params == nil {
		return nil, errors.New("nil RSA parameters")
	}
	if params.Modulus == "" {
		return nil, errors.New("missing modulus")
	}
	if params.Exponent == "" {
		return nil, errors.New("missing exponent")
	}
	if params.D == "" {
		return nil, errors.New("missing d")
	}

	modBytes, err := decodeBase64(params.Modulus)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	expBytes, err := decodeBase64(params.Exponent)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	dBytes, err := decodeBase64(params.D)
	if err != nil {
		return nil, fmt.Errorf("decode d: %w", err)
	}

	n := new(big.Int).SetBytes(modBytes)
	e := int(new(big.Int).SetBytes(expBytes).Int64())
	if e == 0 {
		return nil, errors.New("invalid exponent")
	}
	d := new(big.Int).SetBytes(dBytes)

	var primes []*big.Int
	if params.P != "" || params.Q != "" {
		if params.P == "" || params.Q == "" {
			return nil, errors.New("both p and q must be provided")
		}
		pBytes, err := decodeBase64(params.P)
		if err != nil {
			return nil, fmt.Errorf("decode p: %w", err)
		}
		qBytes, err := decodeBase64(params.Q)
		if err != nil {
			return nil, fmt.Errorf("decode q: %w", err)
		}
		primes = []*big.Int{new(big.Int).SetBytes(pBytes), new(big.Int).SetBytes(qBytes)}
	}

	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{
			N: n,
			E: e,
		},
		D:      d,
		Primes: primes,
	}

	if params.DP != "" && params.DQ != "" && params.InverseQ != "" {
		dpBytes, err := decodeBase64(params.DP)
		if err != nil {
			return nil, fmt.Errorf("decode dp: %w", err)
		}
		dqBytes, err := decodeBase64(params.DQ)
		if err != nil {
			return nil, fmt.Errorf("decode dq: %w", err)
		}
		invqBytes, err := decodeBase64(params.InverseQ)
		if err != nil {
			return nil, fmt.Errorf("decode inverseQ: %w", err)
		}

		key.Precomputed.Dp = new(big.Int).SetBytes(dpBytes)
		key.Precomputed.Dq = new(big.Int).SetBytes(dqBytes)
		key.Precomputed.Qinv = new(big.Int).SetBytes(invqBytes)
	}

	if len(key.Primes) >= 2 {
		key.Precompute()
	}

	return key, nil
}

// ExportRSAParameters converts a Go crypto/rsa PrivateKey into .NET RSAParameters.
func ExportRSAParameters(key *rsa.PrivateKey) *RSAParameters {
	if key == nil {
		return nil
	}

	if len(key.Primes) >= 2 && (key.Precomputed.Dp == nil || key.Precomputed.Dq == nil || key.Precomputed.Qinv == nil) {
		key.Precompute()
	}

	params := &RSAParameters{}
	if key.N != nil {
		params.Modulus = base64.StdEncoding.EncodeToString(key.N.Bytes())
	}
	if key.E > 0 {
		params.Exponent = base64.StdEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	}
	if key.D != nil {
		params.D = base64.StdEncoding.EncodeToString(key.D.Bytes())
	}
	if len(key.Primes) > 0 && key.Primes[0] != nil {
		params.P = base64.StdEncoding.EncodeToString(key.Primes[0].Bytes())
	}
	if len(key.Primes) > 1 && key.Primes[1] != nil {
		params.Q = base64.StdEncoding.EncodeToString(key.Primes[1].Bytes())
	}
	if key.Precomputed.Dp != nil {
		params.DP = base64.StdEncoding.EncodeToString(key.Precomputed.Dp.Bytes())
	}
	if key.Precomputed.Dq != nil {
		params.DQ = base64.StdEncoding.EncodeToString(key.Precomputed.Dq.Bytes())
	}
	if key.Precomputed.Qinv != nil {
		params.InverseQ = base64.StdEncoding.EncodeToString(key.Precomputed.Qinv.Bytes())
	}

	return params
}

func generateUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// AcquireOAuthToken exchanges a PS256-signed JWT assertion for an OAuth access token from authURL.
func AcquireOAuthToken(ctx context.Context, client *http.Client, authURL, clientID string, key *rsa.PrivateKey) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if authURL == "" {
		return "", errors.New("empty auth URL")
	}
	if clientID == "" {
		return "", errors.New("empty client ID")
	}
	if key == nil {
		return "", errors.New("nil RSA private key")
	}

	jti, err := generateUUID()
	if err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}

	now := time.Now().UTC().Unix()
	claims := jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": authURL,
		"jti": jti,
		"iat": now,
		"nbf": now - 30,
		"exp": now + 240,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodPS256, claims)
	assertion, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign assertion: %w", err)
	}

	form := url.Values{}
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("unmarshal token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", errors.New("empty access token in response")
	}

	return tokenResp.AccessToken, nil
}
