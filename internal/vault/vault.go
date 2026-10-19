package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	"github.com/SilkageNet/codex-switch/internal/secretstore"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const associatedData = "codex-switch:vault:v1"

type Profile struct {
	ID              string          `json:"id"`
	Alias           string          `json:"alias"`
	AccountID       string          `json:"accountId"`
	WorkspaceID     string          `json:"workspaceId,omitempty"`
	Email           string          `json:"email,omitempty"`
	Auth            json.RawMessage `json:"auth"`
	Source          string          `json:"source"`
	CreatedAt       time.Time       `json:"createdAt"`
	AuthenticatedAt time.Time       `json:"authenticatedAt"`
	LastUsedAt      time.Time       `json:"lastUsedAt,omitempty"`
	TokenUpdatedAt  time.Time       `json:"tokenUpdatedAt,omitempty"`
}

type Data struct {
	Version   int       `json:"version"`
	Profiles  []Profile `json:"profiles"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type envelope struct {
	Version    int    `json:"version"`
	Cipher     string `json:"cipher"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type portableEnvelope struct {
	Version     int    `json:"version"`
	KDF         string `json:"kdf"`
	Time        uint32 `json:"time"`
	MemoryKiB   uint32 `json:"memoryKiB"`
	Parallelism uint8  `json:"parallelism"`
	Salt        string `json:"salt"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

type Manager struct {
	path  string
	store secretstore.Store
}

func New(path string, store secretstore.Store) *Manager {
	return &Manager{path: path, store: store}
}

func (manager *Manager) Init() (Data, error) {
	// Initialization runs before operation locking, including on an empty home.
	if err := os.MkdirAll(filepath.Dir(manager.path), 0o700); err != nil {
		return Data{}, err
	}
	lock, err := filelock.AcquireVault(manager.path)
	if err != nil {
		return Data{}, err
	}
	defer func() { _ = lock.Close() }()
	data, err := manager.Load()
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Data{}, err
	}
	data = Data{Version: 1, Profiles: []Profile{}}
	if err := manager.saveWithNewKey(data, ""); err != nil {
		return Data{}, err
	}
	return data, nil
}

func (manager *Manager) Load() (Data, error) {
	encoded, err := atomicfile.ReadLimited(manager.path, 32<<20)
	if err != nil {
		return Data{}, err
	}
	var wrapper envelope
	if err := json.Unmarshal(encoded, &wrapper); err != nil {
		return Data{}, fmt.Errorf("decode vault envelope: %w", err)
	}
	if wrapper.Version != 1 || wrapper.Cipher != "xchacha20-poly1305" || wrapper.KeyID == "" {
		return Data{}, errors.New("unsupported vault envelope")
	}
	key, err := manager.readKey(wrapper.KeyID)
	if err != nil {
		return Data{}, err
	}
	plaintext, err := decrypt(key, wrapper.Nonce, wrapper.Ciphertext, []byte(associatedData))
	if err != nil {
		return Data{}, fmt.Errorf("decrypt vault: %w", err)
	}
	return decodeData(plaintext)
}

// Save requires the caller to hold the vault lock from before loading data.
func (manager *Manager) Save(data Data) error {
	encoded, err := atomicfile.ReadLimited(manager.path, 32<<20)
	if errors.Is(err, os.ErrNotExist) {
		return manager.saveWithNewKey(data, "")
	}
	if err != nil {
		return err
	}
	var current envelope
	if err := json.Unmarshal(encoded, &current); err != nil {
		return err
	}
	key, err := manager.readKey(current.KeyID)
	if err != nil {
		return err
	}
	return manager.write(data, current.KeyID, key)
}

// RotateKey requires the caller to hold the vault lock.
func (manager *Manager) RotateKey() error {
	data, err := manager.Load()
	if err != nil {
		return err
	}
	encoded, err := atomicfile.ReadLimited(manager.path, 32<<20)
	if err != nil {
		return err
	}
	var old envelope
	if err := json.Unmarshal(encoded, &old); err != nil {
		return err
	}
	if err := manager.saveWithNewKey(data, old.KeyID); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) saveWithNewKey(data Data, oldKeyID string) error {
	key, err := randomBytes(chacha20poly1305.KeySize)
	if err != nil {
		return err
	}
	keyID, err := randomID(12)
	if err != nil {
		return err
	}
	if err := manager.store.Set("master-key/"+keyID, base64.RawStdEncoding.EncodeToString(key)); err != nil {
		return fmt.Errorf("store vault key: %w", err)
	}
	if err := manager.write(data, keyID, key); err != nil {
		_ = manager.store.Delete("master-key/" + keyID)
		return err
	}
	if oldKeyID != "" && oldKeyID != keyID {
		_ = manager.store.Delete("master-key/" + oldKeyID)
	}
	return nil
}

func (manager *Manager) write(data Data, keyID string, key []byte) error {
	if err := validateData(data); err != nil {
		return err
	}
	data.Version = 1
	data.UpdatedAt = time.Now().UTC()
	data.Profiles = append([]Profile(nil), data.Profiles...)
	sort.SliceStable(data.Profiles, func(i, j int) bool {
		return strings.ToLower(data.Profiles[i].Alias) < strings.ToLower(data.Profiles[j].Alias)
	})
	plaintext, err := json.Marshal(data)
	if err != nil {
		return err
	}
	nonce, ciphertext, err := encrypt(key, plaintext, []byte(associatedData))
	if err != nil {
		return err
	}
	wrapper := envelope{Version: 1, Cipher: "xchacha20-poly1305", KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext}
	encoded, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(manager.path, append(encoded, '\n'), 0o600)
}

func (manager *Manager) readKey(keyID string) ([]byte, error) {
	encoded, err := manager.store.Get("master-key/" + keyID)
	if err != nil {
		return nil, fmt.Errorf("read vault key: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(key) != chacha20poly1305.KeySize {
		return nil, errors.New("stored vault key is invalid")
	}
	return key, nil
}

func (data *Data) Find(aliasOrID string) (*Profile, error) {
	for index := range data.Profiles {
		profile := &data.Profiles[index]
		if profile.ID == aliasOrID || strings.EqualFold(profile.Alias, aliasOrID) {
			return profile, nil
		}
	}
	return nil, fmt.Errorf("account profile %q not found", aliasOrID)
}

func (data *Data) FindByAccount(accountID, workspaceID string) []*Profile {
	var matches []*Profile
	for index := range data.Profiles {
		profile := &data.Profiles[index]
		if profile.AccountID == accountID && (workspaceID == "" || profile.WorkspaceID == workspaceID) {
			matches = append(matches, profile)
		}
	}
	return matches
}

func (data *Data) Add(profile Profile, replace bool) error {
	if err := ValidateAlias(profile.Alias); err != nil {
		return err
	}
	for index := range data.Profiles {
		existing := data.Profiles[index]
		if strings.EqualFold(existing.Alias, profile.Alias) {
			if !replace {
				return fmt.Errorf("account alias %q already exists", profile.Alias)
			}
			profile.ID = existing.ID
			profile.CreatedAt = existing.CreatedAt
			data.Profiles[index] = profile
			return nil
		}
	}
	if profile.ID == "" {
		id, err := randomID(16)
		if err != nil {
			return err
		}
		profile.ID = id
	}
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = time.Now().UTC()
	}
	data.Profiles = append(data.Profiles, profile)
	return nil
}

func (data *Data) Remove(aliasOrID string) (Profile, error) {
	for index, profile := range data.Profiles {
		if profile.ID == aliasOrID || strings.EqualFold(profile.Alias, aliasOrID) {
			data.Profiles = append(data.Profiles[:index], data.Profiles[index+1:]...)
			return profile, nil
		}
	}
	return Profile{}, fmt.Errorf("account profile %q not found", aliasOrID)
}

func NewProfile(alias, source string, auth json.RawMessage, accountID, workspaceID, email string, tokenUpdatedAt time.Time) Profile {
	now := time.Now().UTC()
	return Profile{Alias: alias, Source: source, Auth: append(json.RawMessage(nil), auth...), AccountID: accountID, WorkspaceID: workspaceID, Email: email, CreatedAt: now, AuthenticatedAt: now, TokenUpdatedAt: tokenUpdatedAt}
}

func Export(data Data, passphrase []byte) ([]byte, error) {
	if len(passphrase) < 12 {
		return nil, errors.New("export passphrase must contain at least 12 characters")
	}
	plaintext, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	salt, err := randomBytes(16)
	if err != nil {
		return nil, err
	}
	const timeCost uint32 = 3
	const memory uint32 = 64 * 1024
	const parallelism uint8 = 4
	key := argon2.IDKey(passphrase, salt, timeCost, memory, parallelism, chacha20poly1305.KeySize)
	nonce, ciphertext, err := encrypt(key, plaintext, []byte("codex-switch:portable:v1"))
	if err != nil {
		return nil, err
	}
	wrapper := portableEnvelope{Version: 1, KDF: "argon2id", Time: timeCost, MemoryKiB: memory, Parallelism: parallelism, Salt: base64.RawStdEncoding.EncodeToString(salt), Nonce: nonce, Ciphertext: ciphertext}
	return json.MarshalIndent(wrapper, "", "  ")
}

func Import(encoded, passphrase []byte) (Data, error) {
	var wrapper portableEnvelope
	if err := json.Unmarshal(encoded, &wrapper); err != nil {
		return Data{}, fmt.Errorf("decode portable vault: %w", err)
	}
	if wrapper.Version != 1 || wrapper.KDF != "argon2id" || wrapper.Time == 0 || wrapper.Time > 10 || wrapper.MemoryKiB < 8*1024 || wrapper.MemoryKiB > 1024*1024 || wrapper.Parallelism == 0 || wrapper.Parallelism > 16 {
		return Data{}, errors.New("unsupported portable vault")
	}
	salt, err := base64.RawStdEncoding.DecodeString(wrapper.Salt)
	if err != nil {
		return Data{}, errors.New("portable vault salt is invalid")
	}
	key := argon2.IDKey(passphrase, salt, wrapper.Time, wrapper.MemoryKiB, wrapper.Parallelism, chacha20poly1305.KeySize)
	plaintext, err := decrypt(key, wrapper.Nonce, wrapper.Ciphertext, []byte("codex-switch:portable:v1"))
	if err != nil {
		return Data{}, errors.New("portable vault passphrase or data is invalid")
	}
	return decodeData(plaintext)
}

func encrypt(key, plaintext, additionalData []byte) (string, string, error) {
	cipher, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", "", err
	}
	nonce, err := randomBytes(cipher.NonceSize())
	if err != nil {
		return "", "", err
	}
	sealed := cipher.Seal(nil, nonce, plaintext, additionalData)
	return base64.RawStdEncoding.EncodeToString(nonce), base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decrypt(key []byte, encodedNonce, encodedCiphertext string, additionalData []byte) ([]byte, error) {
	cipher, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.RawStdEncoding.DecodeString(encodedNonce)
	if err != nil || len(nonce) != cipher.NonceSize() {
		return nil, errors.New("encrypted nonce is invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(encodedCiphertext)
	if err != nil {
		return nil, errors.New("encrypted payload is invalid")
	}
	return cipher.Open(nil, nonce, ciphertext, additionalData)
}

func decodeData(plaintext []byte) (Data, error) {
	var data Data
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return Data{}, fmt.Errorf("decode vault data: %w", err)
	}
	if data.Version != 1 {
		return Data{}, fmt.Errorf("unsupported vault data version %d", data.Version)
	}
	if err := validateData(data); err != nil {
		return Data{}, err
	}
	return data, nil
}

func ValidateAlias(alias string) error {
	if alias == "" || alias != strings.TrimSpace(alias) {
		return errors.New("account alias must not be empty or have surrounding whitespace")
	}
	if utf8.RuneCountInString(alias) > 64 {
		return errors.New("account alias must not exceed 64 characters")
	}
	for _, character := range alias {
		if unicode.IsControl(character) {
			return errors.New("account alias must not contain control characters")
		}
	}
	return nil
}

func validateData(data Data) error {
	if len(data.Profiles) > 512 {
		return errors.New("vault contains more than 512 account profiles")
	}
	aliases := make(map[string]struct{}, len(data.Profiles))
	identifiers := make(map[string]struct{}, len(data.Profiles))
	for _, profile := range data.Profiles {
		if err := ValidateAlias(profile.Alias); err != nil {
			return fmt.Errorf("invalid account profile: %w", err)
		}
		if profile.ID == "" || len(profile.ID) > 128 {
			return fmt.Errorf("account profile %q has an invalid identifier", profile.Alias)
		}
		if profile.AccountID == "" {
			return fmt.Errorf("account profile %q has no account identifier", profile.Alias)
		}
		if len(profile.Auth) > 2<<20 {
			return fmt.Errorf("account profile %q exceeds the authentication size limit", profile.Alias)
		}
		aliasKey := strings.ToLower(profile.Alias)
		if _, exists := aliases[aliasKey]; exists {
			return fmt.Errorf("vault contains duplicate alias %q", profile.Alias)
		}
		aliases[aliasKey] = struct{}{}
		if _, exists := identifiers[profile.ID]; exists {
			return fmt.Errorf("vault contains duplicate profile identifier %q", profile.ID)
		}
		identifiers[profile.ID] = struct{}{}
	}
	return nil
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

func randomID(size int) (string, error) {
	value, err := randomBytes(size)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
