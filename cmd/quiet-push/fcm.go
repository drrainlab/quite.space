package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ServiceAccount is the part of Firebase's service-account JSON this needs.
type ServiceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	key         *rsa.PrivateKey
}

// LoadServiceAccount reads and checks the key file. The key is parsed once,
// here, so a bad file is a refusal to start rather than a failure per ping.
func LoadServiceAccount(path string) (*ServiceAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sa ServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("service account: %w", err)
	}
	if sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("service account: project_id, client_email and private_key are required")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account: private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("service account: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account: private_key is not RSA")
	}
	sa.key = rk
	return &sa, nil
}

// FCM sends through Firebase Cloud Messaging's HTTP v1 API, authenticated
// with a short-lived OAuth2 access token minted from the service account.
type FCM struct {
	sa   *ServiceAccount
	http *http.Client
	// sendURL and now are seams for the tests.
	sendURL string
	now     func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// NewFCM builds a sender for the account's project.
func NewFCM(sa *ServiceAccount) *FCM {
	return &FCM{
		sa:      sa,
		http:    &http.Client{Timeout: 10 * time.Second},
		sendURL: "https://fcm.googleapis.com/v1/projects/" + sa.ProjectID + "/messages:send",
		now:     time.Now,
	}
}

// Send forwards one contentless, high-priority wake to a token.
//
// HIGH PRIORITY IS THE POINT: it is what lets the message through Doze and
// grants the app a few seconds of network to collect its mail. Data-only,
// so Android hands it to the app rather than drawing a notification of its
// own; the app draws its own line, or none, from what it then collects.
// The TTL is short — a wake delivered an hour late wakes a phone for mail
// its own poll has long since fetched.
func (f *FCM) Send(token string) Outcome {
	at, err := f.accessToken()
	if err != nil {
		return Refused
	}
	body, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": token,
			"android": map[string]any{
				"priority": "high",
				"ttl":      "120s",
			},
			"data": map[string]string{"qp": "1"},
		},
	})
	req, err := http.NewRequest(http.MethodPost, f.sendURL, bytes.NewReader(body))
	if err != nil {
		return Failed
	}
	req.Header.Set("Authorization", "Bearer "+at)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return Failed
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 200:
		return Delivered
	case resp.StatusCode == 404:
		// UNREGISTERED: the app was removed or the token rotated.
		return Gone
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		f.mu.Lock()
		f.token, f.expires = "", time.Time{} // do not trust the cached token again
		f.mu.Unlock()
		return Refused
	case resp.StatusCode == 400:
		// INVALID_ARGUMENT is almost always a malformed token.
		return Gone
	default:
		return Failed
	}
}

// accessToken returns a cached token or mints one: a JWT signed with the
// service-account key, exchanged at Google's token endpoint. Tokens live an
// hour; this renews a minute early.
func (f *FCM) accessToken() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if f.token != "" && now.Before(f.expires.Add(-time.Minute)) {
		return f.token, nil
	}
	assertion, err := f.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	resp, err := f.http.PostForm(f.sa.TokenURI, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token endpoint: %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("token endpoint: no access_token")
	}
	f.token = tok.AccessToken
	f.expires = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	return f.token, nil
}

// assertion is the RS256 JWT Google's token endpoint accepts for a
// service account (RFC 7523).
func (f *FCM) assertion(now time.Time) (string, error) {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims := enc(map[string]any{
		"iss":   f.sa.ClientEmail,
		"scope": fcmScope,
		"aud":   f.sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	signing := head + "." + claims
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.sa.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// verifyAssertion is the test's side of the exchange: what Google checks.
func verifyAssertion(jwt string, pub *rsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	return claims, json.Unmarshal(raw, &claims)
}
