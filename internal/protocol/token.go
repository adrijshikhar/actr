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
	"math"
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
	if n.Sign() <= 0 {
		return nil, errors.New("invalid modulus: must be positive")
	}

	expBig := new(big.Int).SetBytes(expBytes)
	if !expBig.IsInt64() {
		return nil, errors.New("invalid exponent: exceeds int64 range")
	}
	exp64 := expBig.Int64()
	if exp64 < 3 || exp64%2 == 0 || exp64 > int64(math.MaxInt) {
		return nil, errors.New("invalid exponent: must be an odd integer >= 3")
	}
	e := int(exp64)

	d := new(big.Int).SetBytes(dBytes)
	if d.Sign() <= 0 {
		return nil, errors.New("invalid d: must be positive")
	}

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
		if err := key.Validate(); err != nil {
			return nil, fmt.Errorf("invalid RSA private key: %w", err)
		}
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
	token, _, err := acquireOAuthTokenWithExpiry(ctx, client, authURL, clientID, key)
	return token, err
}

func acquireOAuthTokenWithExpiry(ctx context.Context, client *http.Client, authURL, clientID string, key *rsa.PrivateKey) (string, time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = newDefaultHTTPClient(30 * time.Second)
	}
	if authURL == "" {
		return "", time.Time{}, errors.New("empty auth URL")
	}
	if _, err := validateRemoteURL(authURL); err != nil {
		return "", time.Time{}, fmt.Errorf("invalid auth URL: %w", err)
	}
	if clientID == "" {
		return "", time.Time{}, errors.New("empty client ID")
	}
	if key == nil {
		return "", time.Time{}, errors.New("nil RSA private key")
	}

	jti, err := generateUUID()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate jti: %w", err)
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
		return "", time.Time{}, fmt.Errorf("sign assertion: %w", err)
	}

	form := url.Values{}
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req) //nolint:bodyclose // drainAndClose drains and closes resp.Body
	if err != nil {
		return "", time.Time{}, fmt.Errorf("execute token request: %w", err)
	}
	defer drainAndClose(resp.Body)

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("token request returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("unmarshal token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", time.Time{}, errors.New("empty access token in response")
	}

	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		maxSeconds := int64(math.MaxInt64 / time.Second)
		expSec := tokenResp.ExpiresIn
		if expSec > maxSeconds {
			expSec = maxSeconds
		}
		expiresAt = time.Now().Add(time.Duration(expSec) * time.Second)
	}

	return tokenResp.AccessToken, expiresAt, nil
}
