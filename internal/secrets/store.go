package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ReferenceName is the reference target that reads secrets, as in
// ${{ secret.DB_PASSWORD }}.
const ReferenceName = "secret"

type ScopeType string

const (
	ScopeWorkspace  ScopeType = "workspace"
	ScopeDeployment ScopeType = "deployment"
	ScopeService    ScopeType = "service"
)

var (
	ErrNotFound    = errors.New("secret not found")
	ErrInvalidName = errors.New("secret names use letters, digits and underscores, and do not start with a digit")

	namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Scope is what a secret or binding belongs to. A service scope is written as
// deployment/service.
type Scope struct {
	Type ScopeType `json:"type"`
	ID   string    `json:"id"`
}

func ParseScope(s string) (Scope, error) {
	typ, id, ok := strings.Cut(s, ":")
	scope := Scope{Type: ScopeType(typ), ID: id}
	if !ok {
		return scope, fmt.Errorf("scope must look like type:id, got %q", s)
	}
	return scope, scope.Validate()
}

func (s Scope) Validate() error {
	if s.ID == "" {
		return errors.New("scope id is required")
	}
	switch s.Type {
	case ScopeWorkspace, ScopeDeployment:
		if strings.Contains(s.ID, "/") {
			return fmt.Errorf("%s scope id cannot contain /", s.Type)
		}
	case ScopeService:
		dep, svc, ok := strings.Cut(s.ID, "/")
		if !ok || dep == "" || svc == "" {
			return errors.New("service scope id must be deployment/service")
		}
	default:
		return fmt.Errorf("unknown scope type %q", s.Type)
	}
	return nil
}

func (s Scope) String() string { return string(s.Type) + ":" + s.ID }

// Deployment is the deployment a deployment or service scope belongs to.
func (s Scope) Deployment() string {
	switch s.Type {
	case ScopeDeployment:
		return s.ID
	case ScopeService:
		dep, _, _ := strings.Cut(s.ID, "/")
		return dep
	}
	return ""
}

// Generator describes how a rotated value is produced.
type Generator struct {
	Length  int    `json:"length"`
	Charset string `json:"charset"`
}

const (
	CharsetAlphanumeric = "alphanumeric"
	CharsetHex          = "hex"
	CharsetSymbols      = "symbols"
)

var charsets = map[string]string{
	CharsetAlphanumeric: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
	CharsetSymbols:      "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~!@#%^*+=",
}

func (g Generator) normalized() (Generator, error) {
	if g.Length == 0 {
		g.Length = 32
	}
	if g.Charset == "" {
		g.Charset = CharsetAlphanumeric
	}
	if g.Length < 8 || g.Length > 256 {
		return g, errors.New("generated secrets are 8 to 256 characters long")
	}
	if g.Charset != CharsetHex && charsets[g.Charset] == "" {
		return g, fmt.Errorf("unknown charset %q", g.Charset)
	}
	return g, nil
}

func (g Generator) Generate() (string, error) {
	g, err := g.normalized()
	if err != nil {
		return "", err
	}
	if g.Charset == CharsetHex {
		raw := make([]byte, (g.Length+1)/2)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		return hex.EncodeToString(raw)[:g.Length], nil
	}
	alphabet := charsets[g.Charset]
	out := make([]byte, g.Length)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// Rotation regenerates a secret on a cron schedule.
type Rotation struct {
	Schedule string `json:"schedule"`
	Generator
}

// Secret is a stored secret without its value. Values only leave the store
// through Value.
type Secret struct {
	Name          string     `json:"name"`
	Scope         Scope      `json:"scope"`
	Version       int        `json:"version"`
	Rotation      *Rotation  `json:"rotation,omitempty"`
	LastRotatedAt *time.Time `json:"last_rotated_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type storedSecret struct {
	Secret
	Value string `json:"value"`
}

// Binding connects a scope to a secret manager. Every key the manager returns
// becomes readable as ${{ secret.KEY }} in that scope.
type Binding struct {
	ID           string            `json:"id"`
	Scope        Scope             `json:"scope"`
	Provider     string            `json:"provider"`
	ConnectionID string            `json:"connection_id"`
	Options      map[string]string `json:"options,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

type storeFile struct {
	Secrets  []storedSecret `json:"secrets"`
	Bindings []Binding      `json:"bindings"`
}

// Store keeps secrets and bindings in one JSON file, with every value sealed
// by the keyring.
type Store struct {
	path    string
	keyring *Keyring
	now     func() time.Time
	mu      sync.Mutex
}

func NewStore(path string, keyring *Keyring) *Store {
	return &Store{path: path, keyring: keyring, now: time.Now}
}

func (s *Store) load() (*storeFile, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &storeFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	return &f, nil
}

func (s *Store) save(f *storeFile) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (f *storeFile) find(scope Scope, name string) int {
	for i, sec := range f.Secrets {
		if sec.Scope == scope && sec.Name == name {
			return i
		}
	}
	return -1
}

func (s *Store) List(scope Scope) ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []Secret{}
	for _, sec := range f.Secrets {
		if sec.Scope == scope {
			out = append(out, sec.Secret)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Set stores a value, creating the secret or raising its version.
func (s *Store) Set(scope Scope, name, value string) (*Secret, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if !namePattern.MatchString(name) {
		return nil, ErrInvalidName
	}
	sealed, err := s.keyring.Seal(value)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	i := f.find(scope, name)
	if i < 0 {
		f.Secrets = append(f.Secrets, storedSecret{Secret: Secret{Name: name, Scope: scope, CreatedAt: now}})
		i = len(f.Secrets) - 1
	}
	f.Secrets[i].Value = sealed
	f.Secrets[i].Version++
	f.Secrets[i].UpdatedAt = now
	if err := s.save(f); err != nil {
		return nil, err
	}
	out := f.Secrets[i].Secret
	return &out, nil
}

func (s *Store) Delete(scope Scope, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	i := f.find(scope, name)
	if i < 0 {
		return ErrNotFound
	}
	f.Secrets = append(f.Secrets[:i], f.Secrets[i+1:]...)
	return s.save(f)
}

func (s *Store) Get(scope Scope, name string) (*Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	i := f.find(scope, name)
	if i < 0 {
		return nil, ErrNotFound
	}
	out := f.Secrets[i].Secret
	return &out, nil
}

// Values returns the plain values of every secret in a scope.
func (s *Store) Values(scope Scope) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, sec := range f.Secrets {
		if sec.Scope != scope {
			continue
		}
		value, err := s.keyring.Open(sec.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sec.Name, err)
		}
		out[sec.Name] = value
	}
	return out, nil
}

// SetRotation sets or clears how a secret rotates.
func (s *Store) SetRotation(scope Scope, name string, rotation *Rotation) (*Secret, error) {
	if rotation != nil {
		gen, err := rotation.Generator.normalized()
		if err != nil {
			return nil, err
		}
		rotation.Generator = gen
		if strings.TrimSpace(rotation.Schedule) == "" {
			return nil, errors.New("a rotation needs a schedule")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	i := f.find(scope, name)
	if i < 0 {
		return nil, ErrNotFound
	}
	f.Secrets[i].Rotation = rotation
	f.Secrets[i].UpdatedAt = s.now().UTC()
	if err := s.save(f); err != nil {
		return nil, err
	}
	out := f.Secrets[i].Secret
	return &out, nil
}

// Rotate replaces a secret's value with a generated one, using its rotation
// settings or the default generator.
func (s *Store) Rotate(scope Scope, name string) (*Secret, error) {
	current, err := s.Get(scope, name)
	if err != nil {
		return nil, err
	}
	gen := Generator{}
	if current.Rotation != nil {
		gen = current.Rotation.Generator
	}
	value, err := gen.Generate()
	if err != nil {
		return nil, err
	}
	if _, err := s.Set(scope, name, value); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	i := f.find(scope, name)
	if i < 0 {
		return nil, ErrNotFound
	}
	now := s.now().UTC()
	f.Secrets[i].LastRotatedAt = &now
	if err := s.save(f); err != nil {
		return nil, err
	}
	out := f.Secrets[i].Secret
	return &out, nil
}

// Rotating lists every secret that has a rotation schedule.
func (s *Store) Rotating() ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []Secret{}
	for _, sec := range f.Secrets {
		if sec.Rotation != nil {
			out = append(out, sec.Secret)
		}
	}
	return out, nil
}

func (s *Store) Bindings(scope Scope) ([]Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []Binding{}
	for _, b := range f.Bindings {
		if b.Scope == scope {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *Store) Binding(id string) (*Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, b := range f.Bindings {
		if b.ID == id {
			out := b
			return &out, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) AddBinding(b Binding) (*Binding, error) {
	if err := b.Scope.Validate(); err != nil {
		return nil, err
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	b.ID = hex.EncodeToString(raw)
	b.CreatedAt = s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	f.Bindings = append(f.Bindings, b)
	if err := s.save(f); err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteBinding(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i, b := range f.Bindings {
		if b.ID == id {
			f.Bindings = append(f.Bindings[:i], f.Bindings[i+1:]...)
			return s.save(f)
		}
	}
	return ErrNotFound
}

// ConnectionInUse reports whether any binding uses a connection.
func (s *Store) ConnectionInUse(connectionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return false, err
	}
	for _, b := range f.Bindings {
		if b.ConnectionID == connectionID {
			return true, nil
		}
	}
	return false, nil
}

// DropScope removes every secret and binding of a scope, for a deleted
// deployment or workspace.
func (s *Store) DropScope(scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	secrets := f.Secrets[:0]
	for _, sec := range f.Secrets {
		if sec.Scope != scope && !(scope.Type == ScopeDeployment && sec.Scope.Type == ScopeService && sec.Scope.Deployment() == scope.ID) {
			secrets = append(secrets, sec)
		}
	}
	bindings := f.Bindings[:0]
	for _, b := range f.Bindings {
		if b.Scope != scope {
			bindings = append(bindings, b)
		}
	}
	f.Secrets, f.Bindings = secrets, bindings
	return s.save(f)
}
