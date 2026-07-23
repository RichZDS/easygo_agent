package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const DefaultTTL = 24 * time.Hour

type Claims struct {
	Subject string `json:"sub"`
	Expires int64  `json:"exp"`
	Issued  int64  `json:"iat"`
}

type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT HS256 secret must be at least 32 bytes")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

func (i *Issuer) Issue(userID uint64) (string, error) {
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(Claims{Subject: strconv.FormatUint(userID, 10), Issued: now, Expires: now + int64(i.ttl.Seconds())})
	if err != nil {
		return "", err
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signed + "." + i.signature(signed), nil
}

func (i *Issuer) Verify(token string) (uint64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || !hmac.Equal([]byte(parts[2]), []byte(i.signature(parts[0]+"."+parts[1]))) {
		return 0, fmt.Errorf("invalid JWT signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, fmt.Errorf("decode JWT claims: %w", err)
	}
	if claims.Expires <= time.Now().Unix() {
		return 0, fmt.Errorf("JWT expired")
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		return 0, fmt.Errorf("invalid JWT subject")
	}
	return userID, nil
}

func (i *Issuer) signature(value string) string {
	h := hmac.New(sha256.New, i.secret)
	_, _ = h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
