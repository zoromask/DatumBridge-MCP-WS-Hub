package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = bcrypt.DefaultCost

// DeviceCredential holds the server-generated secret for a device.
// Token is plain-text only in memory during registration; stored as bcrypt hash.
type DeviceCredential struct {
	DeviceID string
	Token    string
}

// credentialStore maps device_id -> bcrypt-hashed token.
type credentialStore struct {
	mu       sync.RWMutex
	tokens   map[string]string // device_id -> bcrypt hash
	filePath string
}

// pairingEntry holds a pending pairing (6-digit code -> credential)
type pairingEntry struct {
	cred    *DeviceCredential
	expires time.Time
}

// pairingStore maps 6-digit pairing_code -> pairingEntry
type pairingStore struct {
	mu    sync.RWMutex
	store map[string]*pairingEntry
}

func newCredentialStore(filePath string) *credentialStore {
	s := &credentialStore{
		tokens:   make(map[string]string),
		filePath: filePath,
	}
	if filePath != "" {
		if err := s.load(); err != nil {
			log.Warn().Err(err).Str("file", filePath).Msg("failed to load credentials file")
		}
	}
	return s
}

func (s *credentialStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if payload == nil {
		payload = make(map[string]string)
	}

	// Migrate any plain-text tokens to bcrypt hashes on load
	migrated := false
	for k, v := range payload {
		if !isBcryptHash(v) {
			hash, err := bcrypt.GenerateFromPassword([]byte(v), bcryptCost)
			if err != nil {
				log.Error().Err(err).Str("device_id", k).Msg("failed to migrate token to bcrypt")
				continue
			}
			payload[k] = string(hash)
			migrated = true
		}
	}

	s.mu.Lock()
	s.tokens = payload
	s.mu.Unlock()

	if migrated {
		if err := s.save(); err != nil {
			log.Error().Err(err).Msg("failed to persist migrated credentials")
		} else {
			log.Info().Msg("migrated plain-text tokens to bcrypt hashes")
		}
	}
	return nil
}

func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$")
}

func (s *credentialStore) save() error {
	if s.filePath == "" {
		return nil
	}
	s.mu.RLock()
	payload := make(map[string]string, len(s.tokens))
	for k, v := range s.tokens {
		payload[k] = v
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0600)
}

func newPairingStore() *pairingStore {
	return &pairingStore{
		store: make(map[string]*pairingEntry),
	}
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func generatePairingCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// StartPairing creates a pending pairing with a 6-digit code. Expires after 5 minutes.
func (s *credentialStore) StartPairing(deviceID string) (pairingCode string, cred *DeviceCredential, err error) {
	token, err := generateToken()
	if err != nil {
		return "", nil, err
	}
	code, err := generatePairingCode()
	if err != nil {
		return "", nil, err
	}
	if deviceID == "" {
		deviceID = "device-" + token[:12]
	}
	cred = &DeviceCredential{DeviceID: deviceID, Token: token}
	return code, cred, nil
}

func (p *pairingStore) Add(code string, cred *DeviceCredential) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store[code] = &pairingEntry{cred: cred, expires: time.Now().Add(5 * time.Minute)}
}

// PendingPairing is a non-sensitive view of a pending pairing for the UI
type PendingPairing struct {
	PairingCode string    `json:"pairing_code"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (p *pairingStore) List() []PendingPairing {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	var out []PendingPairing
	for code, ent := range p.store {
		if now.Before(ent.expires) {
			out = append(out, PendingPairing{PairingCode: code, ExpiresAt: ent.expires})
		}
	}
	return out
}

func (p *pairingStore) Consume(code string) (*DeviceCredential, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ent, ok := p.store[code]
	if !ok {
		return nil, false
	}
	if time.Now().After(ent.expires) {
		delete(p.store, code)
		return nil, false
	}
	cred := ent.cred
	delete(p.store, code)
	return cred, true
}

// AddCredential hashes the plain token and persists it.
// Must be called with cred.Token as plain text (not yet hashed).
func (s *credentialStore) AddCredential(cred *DeviceCredential) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(cred.Token), bcryptCost)
	if err != nil {
		log.Error().Err(err).Str("device_id", cred.DeviceID).Msg("failed to hash token")
		return err
	}
	s.mu.Lock()
	s.tokens[cred.DeviceID] = string(hash)
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", cred.DeviceID).Msg("failed to persist credential")
		return err
	}
	return nil
}

// RegisterDevice creates a new credential for a device. If device_id is empty, one is generated.
// Returns the plain-text token (shown only once; caller must persist it).
func (s *credentialStore) RegisterDevice(deviceID string) (*DeviceCredential, error) {
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	if deviceID == "" {
		deviceID = "device-" + token[:12]
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hash token: %w", err)
	}

	s.mu.Lock()
	s.tokens[deviceID] = string(hash)
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", deviceID).Msg("failed to persist credential after registration")
	}

	return &DeviceCredential{DeviceID: deviceID, Token: token}, nil
}

// Count returns the number of registered devices (for logging)
func (s *credentialStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// ValidateToken returns true if the token matches the device's stored bcrypt hash.
func (s *credentialStore) ValidateToken(deviceID, token string) bool {
	s.mu.RLock()
	hash, ok := s.tokens[deviceID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil
}

// RevokeDevice removes a device's credential
func (s *credentialStore) RevokeDevice(deviceID string) {
	s.mu.Lock()
	delete(s.tokens, deviceID)
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", deviceID).Msg("failed to persist credential removal")
	}
}
