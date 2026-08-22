package cluster

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/events"
)

type EventPublisher interface {
	Publish(events.Event) (events.IngestResult, error)
}

type PeerStatus struct {
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
	Error    string    `json:"error,omitempty"`
}

type Result struct {
	Data  []byte `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

type Manager struct {
	db             *DB
	clients        map[string]*Client
	status         map[string]*PeerStatus
	mu             sync.RWMutex
	serverName     string
	healthInterval time.Duration
	requestTimeout time.Duration
	encryptionKey  []byte
	publisher      EventPublisher
	cancel         context.CancelFunc
}

func NewManager(db *DB, serverName string, healthInterval, requestTimeout time.Duration, jwtSecret string) *Manager {
	key := sha256.Sum256([]byte(jwtSecret))
	return &Manager{
		db:             db,
		clients:        make(map[string]*Client),
		status:         make(map[string]*PeerStatus),
		serverName:     serverName,
		healthInterval: healthInterval,
		requestTimeout: requestTimeout,
		encryptionKey:  key[:],
	}
}

func (m *Manager) SetEventPublisher(publisher EventPublisher) {
	m.mu.Lock()
	m.publisher = publisher
	m.mu.Unlock()
}

func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancel = context.WithCancel(ctx)

	peers, err := m.db.ListPeers()
	if err != nil {
		return fmt.Errorf("failed to load peers: %w", err)
	}

	m.mu.Lock()
	for _, p := range peers {
		if p.Status != "active" {
			continue
		}
		apiKey, err := m.decrypt(p.APIKeyEncrypted)
		if err != nil {
			log.Printf("Warning: Failed to decrypt API key for peer %s: %v", p.Name, err)
			continue
		}
		m.clients[p.Name] = NewClient(p.URL, apiKey, m.requestTimeout)
		m.status[p.Name] = &PeerStatus{
			Name:     p.Name,
			URL:      p.URL,
			LastSeen: p.LastSeenAt,
		}
	}
	m.mu.Unlock()

	go m.healthLoop(ctx)
	return nil
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Manager) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(m.healthInterval)
	defer ticker.Stop()

	m.checkAllPeers(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAllPeers(ctx)
		}
	}
}

func (m *Manager) checkAllPeers(ctx context.Context) {
	m.mu.RLock()
	type peerEntry struct {
		name   string
		client *Client
	}
	peers := make([]peerEntry, 0, len(m.clients))
	for name, client := range m.clients {
		peers = append(peers, peerEntry{name, client})
	}
	m.mu.RUnlock()

	var seenNames []string

	for _, p := range peers {
		if ctx.Err() != nil {
			return
		}

		err := p.client.Health(ctx)

		m.mu.Lock()
		st, exists := m.status[p.name]
		wasOnline := exists && st.Online
		wasKnown := exists && (st.Online || st.Error != "" || !st.LastSeen.IsZero())
		publisher := m.publisher
		if exists {
			if err != nil {
				st.Online = false
				st.Error = err.Error()
			} else {
				st.Online = true
				st.Error = ""
				st.LastSeen = time.Now()
				seenNames = append(seenNames, p.name)
			}
		}
		m.mu.Unlock()

		event := fleetHealthEvent(p.name, wasKnown, wasOnline, err)
		if publisher == nil || event == nil {
			continue
		}
		if _, publishErr := publisher.Publish(*event); publishErr != nil {
			log.Printf("Warning: Failed to publish Fleet health event for %s: %v", p.name, publishErr)
		}
	}

	for _, name := range seenNames {
		_ = m.db.UpdateLastSeen(name)
	}
}

func fleetHealthEvent(name string, wasKnown, wasOnline bool, healthErr error) *events.Event {
	isOnline := healthErr == nil
	if (!wasKnown && isOnline) || (wasKnown && wasOnline == isOnline) {
		return nil
	}
	event := &events.Event{
		Source: "fleet", Scope: events.Scope{Node: name}, CorrelationKey: "node:" + name, OccurredAt: time.Now(),
	}
	if healthErr != nil {
		event.Type = "node.unavailable"
		event.Severity = events.SeverityCritical
		event.Title = name + " is unavailable"
		event.Message = "The Fleet node stopped responding. Related deployment failures will be grouped into this incident."
		return event
	}
	event.Type = "node.recovered"
	event.Severity = events.SeverityInfo
	event.Title = name + " recovered"
	event.Message = "The Fleet node is responding again."
	event.Resolved = true
	return event
}

func (m *Manager) GetPeer(name string) (*Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[name]
	if !ok {
		return nil, fmt.Errorf("peer %q not found", name)
	}
	return client, nil
}

func (m *Manager) ListPeers() []PeerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]PeerStatus, 0, len(m.status))
	for _, s := range m.status {
		result = append(result, *s)
	}
	return result
}

func (m *Manager) AddPeer(name, url, apiKey string) error {
	encrypted, err := m.encrypt(apiKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt API key: %w", err)
	}

	peer := &Peer{
		Name:            name,
		URL:             url,
		APIKeyHash:      HashToken(apiKey),
		APIKeyEncrypted: encrypted,
		Status:          "active",
	}

	if _, err := m.db.CreatePeer(peer); err != nil {
		return fmt.Errorf("failed to store peer: %w", err)
	}

	m.mu.Lock()
	m.clients[name] = NewClient(url, apiKey, m.requestTimeout)
	m.status[name] = &PeerStatus{
		Name:   name,
		URL:    url,
		Online: false,
	}
	m.mu.Unlock()

	return nil
}

func (m *Manager) RemovePeer(name string) error {
	if err := m.db.DeletePeer(name); err != nil {
		return fmt.Errorf("failed to delete peer: %w", err)
	}

	m.mu.Lock()
	delete(m.clients, name)
	delete(m.status, name)
	m.mu.Unlock()

	return nil
}

func (m *Manager) ForEachPeer(ctx context.Context, fn func(ctx context.Context, name string, client *Client) ([]byte, error)) map[string]Result {
	m.mu.RLock()
	peers := make(map[string]*Client, len(m.clients))
	for n, c := range m.clients {
		peers[n] = c
	}
	m.mu.RUnlock()

	results := make(map[string]Result, len(peers))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, client := range peers {
		wg.Add(1)
		go func(n string, c *Client) {
			defer wg.Done()

			data, err := fn(ctx, n, c)

			mu.Lock()
			if err != nil {
				results[n] = Result{Error: err.Error()}
			} else {
				results[n] = Result{Data: data}
			}
			mu.Unlock()
		}(name, client)
	}

	wg.Wait()
	return results
}

func (m *Manager) ServerName() string {
	return m.serverName
}

func (m *Manager) DB() *DB {
	return m.db
}

func (m *Manager) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(m.encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (m *Manager) decrypt(encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(m.encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
