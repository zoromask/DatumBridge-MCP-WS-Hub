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
	DeviceID   string
	Token      string
	DeviceName string
	DeviceIP   string
	DeviceMAC  string
}

// deviceRecord is the on-disk representation per device.
type deviceRecord struct {
	TokenHash    string `json:"token_hash"`
	DeviceName   string `json:"device_name,omitempty"`
	DeviceIP     string `json:"device_ip,omitempty"`
	DeviceMAC    string `json:"device_mac,omitempty"`
	RegisteredAt string `json:"registered_at,omitempty"`
}

// credentialStore maps device_id -> deviceRecord (bcrypt-hashed token + metadata).
type credentialStore struct {
	mu       sync.RWMutex
	devices  map[string]*deviceRecord
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
		devices:  make(map[string]*deviceRecord),
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

	// Try new format first: map[string]*deviceRecord
	var newPayload map[string]*deviceRecord
	if err := json.Unmarshal(data, &newPayload); err == nil && isNewFormat(newPayload) {
		migrated := false
		for k, rec := range newPayload {
			if !isBcryptHash(rec.TokenHash) {
				hash, err := bcrypt.GenerateFromPassword([]byte(rec.TokenHash), bcryptCost)
				if err != nil {
					log.Error().Err(err).Str("device_id", k).Msg("failed to migrate token to bcrypt")
					continue
				}
				rec.TokenHash = string(hash)
				migrated = true
			}
		}
		s.mu.Lock()
		s.devices = newPayload
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

	// Fallback: old format map[string]string (device_id -> token/hash)
	var oldPayload map[string]string
	if err := json.Unmarshal(data, &oldPayload); err != nil {
		return err
	}
	devices := make(map[string]*deviceRecord, len(oldPayload))
	for k, v := range oldPayload {
		tokenHash := v
		if !isBcryptHash(v) {
			hash, err := bcrypt.GenerateFromPassword([]byte(v), bcryptCost)
			if err != nil {
				log.Error().Err(err).Str("device_id", k).Msg("failed to migrate token to bcrypt")
				continue
			}
			tokenHash = string(hash)
		}
		devices[k] = &deviceRecord{
			TokenHash:    tokenHash,
			RegisteredAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	s.mu.Lock()
	s.devices = devices
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Msg("failed to persist migrated credentials to new format")
	} else {
		log.Info().Int("devices", len(devices)).Msg("migrated credentials from old format to new format")
	}
	return nil
}

// isNewFormat checks if the unmarshalled payload looks like new deviceRecord format
func isNewFormat(payload map[string]*deviceRecord) bool {
	for _, rec := range payload {
		if rec != nil && rec.TokenHash != "" {
			return true
		}
	}
	return len(payload) == 0
}

func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$")
}

func (s *credentialStore) save() error {
	if s.filePath == "" {
		return nil
	}
	s.mu.RLock()
	payload := make(map[string]*deviceRecord, len(s.devices))
	for k, v := range s.devices {
		cp := *v
		payload[k] = &cp
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

// PairingTTL returns how long a pairing code stays valid.
// Override with HUB_PAIRING_TTL (Go duration: 5m, 10m, 1h). Invalid or empty uses 5m.
func PairingTTL() time.Duration {
	s := strings.TrimSpace(os.Getenv("HUB_PAIRING_TTL"))
	if s == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// StartPairing creates a pending pairing with a 6-digit code. Caller must pairingStore.Add(..., expiresAt) using PairingTTL().
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

func (p *pairingStore) Add(code string, cred *DeviceCredential, expiresAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store[code] = &pairingEntry{cred: cred, expires: expiresAt}
}

// PendingPairing is a non-sensitive view of a pending pairing for the UI
type PendingPairing struct {
	DeviceID    string    `json:"device_id"`
	PairingCode string    `json:"pairing_code"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (p *pairingStore) List() []PendingPairing {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	var out []PendingPairing
	for code, ent := range p.store {
		if !now.Before(ent.expires) {
			delete(p.store, code)
			continue
		}
		did := ""
		if ent.cred != nil {
			did = ent.cred.DeviceID
		}
		out = append(out, PendingPairing{DeviceID: did, PairingCode: code, ExpiresAt: ent.expires})
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

// AddCredential hashes the plain token and persists it with device metadata.
// Must be called with cred.Token as plain text (not yet hashed).
func (s *credentialStore) AddCredential(cred *DeviceCredential) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(cred.Token), bcryptCost)
	if err != nil {
		log.Error().Err(err).Str("device_id", cred.DeviceID).Msg("failed to hash token")
		return err
	}
	rec := &deviceRecord{
		TokenHash:    string(hash),
		DeviceName:   cred.DeviceName,
		DeviceIP:     cred.DeviceIP,
		DeviceMAC:    cred.DeviceMAC,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	s.devices[cred.DeviceID] = rec
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", cred.DeviceID).Msg("failed to persist credential")
		return err
	}
	return nil
}

// RegisterDevice creates a new credential for a device with optional metadata.
// Returns the plain-text token (shown only once; caller must persist it).
func (s *credentialStore) RegisterDevice(deviceID, deviceName, deviceIP, deviceMAC string) (*DeviceCredential, error) {
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

	rec := &deviceRecord{
		TokenHash:    string(hash),
		DeviceName:   deviceName,
		DeviceIP:     deviceIP,
		DeviceMAC:    deviceMAC,
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	s.devices[deviceID] = rec
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", deviceID).Msg("failed to persist credential after registration")
	}

	return &DeviceCredential{
		DeviceID:   deviceID,
		Token:      token,
		DeviceName: deviceName,
		DeviceIP:   deviceIP,
		DeviceMAC:  deviceMAC,
	}, nil
}

// Count returns the number of registered devices (for logging)
func (s *credentialStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.devices)
}

// ValidateToken returns true if the token matches the device's stored bcrypt hash.
func (s *credentialStore) ValidateToken(deviceID, token string) bool {
	s.mu.RLock()
	rec, ok := s.devices[deviceID]
	s.mu.RUnlock()
	if !ok || rec == nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(rec.TokenHash), []byte(token)) == nil
}

// GetDeviceRecord returns a copy of the device record, or nil if not found.
func (s *credentialStore) GetDeviceRecord(deviceID string) *deviceRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.devices[deviceID]
	if !ok || rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

// ListAllDeviceRecords returns all device_id -> record pairs (for the list endpoint).
func (s *credentialStore) ListAllDeviceRecords() map[string]*deviceRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*deviceRecord, len(s.devices))
	for k, v := range s.devices {
		cp := *v
		out[k] = &cp
	}
	return out
}

// RevokeDevice removes a device's credential
func (s *credentialStore) RevokeDevice(deviceID string) {
	s.mu.Lock()
	delete(s.devices, deviceID)
	s.mu.Unlock()
	if err := s.save(); err != nil {
		log.Error().Err(err).Str("device_id", deviceID).Msg("failed to persist credential removal")
	}
}
