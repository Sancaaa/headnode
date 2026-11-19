package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"headnode/protocol"
)

// enrollTokenLifetime matches architecture.md §5: valid 24 h, reusable.
const enrollTokenLifetime = 24 * time.Hour

// ErrInvalidToken covers both "unknown" and "expired" on purpose: telling
// them apart would help someone who is guessing tokens.
var ErrInvalidToken = errors.New("invalid or expired enroll token")

// randomHex returns nBytes of randomness as 2*nBytes hex characters.
func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	// crypto/rand, never math/rand: math/rand is predictable, so an attacker
	// who sees a few tokens could guess the next ones.
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// newSecret returns 256 random bits as 64 hex characters. Used for enroll
// tokens and agent secrets now, and for login sessions later (D-09).
func newSecret() (string, error) {
	return randomHex(32)
}

// hashSecret returns the SHA-256 of a secret as hex. A fast hash is enough
// here because the input is already 256 random bits, not a guessable
// password (D-09).
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateEnrollToken stores a new token and returns the plain text once.
// After this call the plain text exists nowhere on the server, so the
// caller must show it to the admin immediately.
func CreateEnrollToken(db *sql.DB) (token string, expiresAt time.Time, err error) {
	return createEnrollToken(db, sql.NullInt64{}) // from the command line: no group
}

// createEnrollToken is CreateEnrollToken for a given group. A host that
// enrolls with the token joins that group. A group id that is not Valid means
// "no group": those hosts start out visible to admins only.
func createEnrollToken(db *sql.DB, groupID sql.NullInt64) (token string, expiresAt time.Time, err error) {
	token, err = newSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expiresAt = now.Add(enrollTokenLifetime)

	_, err = db.Exec(
		`INSERT INTO enroll_tokens (token_hash, group_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashSecret(token), groupID, expiresAt.Unix(), now.Unix(),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("store enroll token: %w", err)
	}
	return token, expiresAt, nil
}

// EnrollAgent checks the token and registers a new agent. The returned secret
// is the only time the plain text is available; only its hash is stored.
// The token is not consumed: it stays valid for 24 h so one token can enroll
// many hosts.
func EnrollAgent(db *sql.DB, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	// The token also says which group the new host joins.
	var groupID sql.NullInt64
	err := db.QueryRow(
		`SELECT group_id FROM enroll_tokens WHERE token_hash = ? AND expires_at > ?`,
		hashSecret(req.Token), time.Now().Unix(),
	).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.EnrollResponse{}, ErrInvalidToken
	}
	if err != nil {
		return protocol.EnrollResponse{}, fmt.Errorf("look up enroll token: %w", err)
	}

	suffix, err := randomHex(8) // the id is not a secret, it only has to be unique
	if err != nil {
		return protocol.EnrollResponse{}, err
	}
	id := "a_" + suffix
	secret, err := newSecret()
	if err != nil {
		return protocol.EnrollResponse{}, err
	}

	_, err = db.Exec(
		`INSERT INTO agents (id, group_id, name, secret_hash, hostname, os, arch, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, groupID, req.Hostname, hashSecret(secret), req.Hostname, req.OS, req.Arch, time.Now().Unix(),
	)
	if err != nil {
		return protocol.EnrollResponse{}, fmt.Errorf("store agent: %w", err)
	}
	return protocol.EnrollResponse{AgentID: id, AgentSecret: secret}, nil
}
