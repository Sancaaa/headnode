package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// RFC 6238 defaults supported by every authenticator app (Google Authenticator,
	// 1Password, Authy, etc.).
	totpPeriod = 30
	totpDigits = 6
	totpIssuer = "headnode"
)

var totpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a 160-bit random secret encoded as unpadded
// uppercase Base32 (the key size RFC 6238 recommends for HMAC-SHA1).
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return totpBase32.EncodeToString(raw), nil
}

// TOTPURI builds the otpauth:// URI that authenticator apps import.
func TOTPURI(issuer, username, secret string) string {
	return fmt.Sprintf(
		"otpauth://totp/%s:%s?secret=%s&issuer=%s",
		url.PathEscape(issuer),
		url.PathEscape(username),
		url.QueryEscape(secret),
		url.QueryEscape(issuer),
	)
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	clean := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(strings.TrimSpace(secret)))
	if clean == "" {
		return nil, errors.New("empty TOTP secret")
	}
	key, err := totpBase32.DecodeString(clean)
	if err != nil || len(key) == 0 {
		return nil, errors.New("invalid TOTP secret")
	}
	return key, nil
}

func totpAtStep(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", bin%1_000_000)
}

// TOTPCode returns the 6-digit RFC 6238 TOTP code for secret at time at.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	if at.Unix() < 0 {
		return "", errors.New("invalid TOTP timestamp")
	}
	return totpAtStep(key, at.Unix()/totpPeriod), nil
}

// VerifyTOTP checks a user-entered 6-digit code against secret across the
// current 30-second step and one step on either side (clock skew tolerance).
// On success it returns the matched time step so the caller can reject replays.
func VerifyTOTP(secret, code string, now time.Time) (int64, bool) {
	clean := strings.NewReplacer(" ", "", "-", "").Replace(code)
	if len(clean) != totpDigits {
		return 0, false
	}
	for i := 0; i < len(clean); i++ {
		if clean[i] < '0' || clean[i] > '9' {
			return 0, false
		}
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil || now.Unix() < 0 {
		return 0, false
	}

	nowStep := now.Unix() / totpPeriod
	var matchedStep int64
	var matched bool
	for delta := int64(-1); delta <= 1; delta++ {
		step := nowStep + delta
		if step < 0 {
			continue
		}
		expected := totpAtStep(key, step)
		if subtle.ConstantTimeCompare([]byte(clean), []byte(expected)) == 1 {
			matchedStep = step
			matched = true
		}
	}
	return matchedStep, matched
}
