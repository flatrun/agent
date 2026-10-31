package securityreview

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/secrets"
	"github.com/flatrun/agent/pkg/securityprofile"
)

type Envelope struct {
	APIVersion         string            `json:"api_version"`
	Source             string            `json:"source"`
	SourceVersion      string            `json:"source_version"`
	Deployment         string            `json:"deployment"`
	Run                string            `json:"run"`
	ProfileRevision    string            `json:"profile_revision"`
	DeploymentRevision string            `json:"deployment_revision"`
	WindowStart        time.Time         `json:"window_start"`
	WindowEnd          time.Time         `json:"window_end"`
	Cursor             int64             `json:"cursor"`
	NextCursor         int64             `json:"next_cursor"`
	Count              int               `json:"count"`
	Total              int               `json:"total"`
	Final              bool              `json:"final"`
	WindowComplete     bool              `json:"window_complete"`
	Items              []json.RawMessage `json:"items"`
	KeyID              string            `json:"key_id"`
	MAC                string            `json:"mac"`
}
type EvidenceKeyring struct {
	store *secrets.Store
	mu    sync.Mutex
}

var keyScope = secrets.Scope{Type: secrets.ScopeWorkspace, ID: "security-evidence"}

func OpenEvidenceKeys(root string) (*EvidenceKeyring, error) {
	keyring, err := secrets.LoadKeyring(filepath.Join(root, "evidence-keyring.json"))
	if err != nil {
		return nil, err
	}
	return &EvidenceKeyring{store: secrets.NewStore(filepath.Join(root, "evidence-keys.json"), keyring)}, nil
}
func (k *EvidenceKeyring) EnsureKey(id string) error {
	if !securityprofile.ValidIdentifier(id) {
		return fmt.Errorf("invalid evidence key id")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	values, err := k.store.Values(keyScope)
	if err != nil {
		return err
	}
	name := "KEY_" + strings.ReplaceAll(id, "-", "_")
	if values[name] != "" {
		return nil
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	_, err = k.store.Set(keyScope, name, hex.EncodeToString(key))
	return err
}
func (k *EvidenceKeyring) key(id string) ([]byte, error) {
	values, err := k.store.Values(keyScope)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(values["KEY_"+strings.ReplaceAll(id, "-", "_")])
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("unknown evidence key")
	}
	return key, nil
}
func canonicalEnvelope(e Envelope) ([]byte, error) {
	e.MAC = ""
	items := make([]json.RawMessage, len(e.Items))
	for i, item := range e.Items {
		var v any
		decoder := json.NewDecoder(strings.NewReader(string(item)))
		decoder.UseNumber()
		if err := decoder.Decode(&v); err != nil {
			return nil, err
		}
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		items[i] = data
	}
	e.Items = items
	return json.Marshal(e)
}
func (k *EvidenceKeyring) Sign(e *Envelope, id string) error {
	key, err := k.key(id)
	if err != nil {
		return err
	}
	e.KeyID = id
	data, err := canonicalEnvelope(*e)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	e.MAC = hex.EncodeToString(mac.Sum(nil))
	return nil
}
func (k *EvidenceKeyring) Verify(e Envelope, expected Envelope) error {
	if e.APIVersion != "flatrun.security-evidence/v1" || e.Source != expected.Source || e.SourceVersion != expected.SourceVersion || e.Deployment != expected.Deployment || e.Run != expected.Run || e.ProfileRevision != expected.ProfileRevision || e.DeploymentRevision != expected.DeploymentRevision || !e.WindowStart.Equal(expected.WindowStart) || !e.WindowEnd.Equal(expected.WindowEnd) || !e.WindowEnd.After(e.WindowStart) || e.Cursor != expected.Cursor || e.Total != expected.Total || e.Count != len(e.Items) || e.Count > 1000 || e.NextCursor != e.Cursor+int64(e.Count) || e.Cursor < 0 || e.NextCursor > int64(e.Total) || (e.Count == 0 && !e.Final) || e.Final != (e.NextCursor == int64(e.Total)) {
		return fmt.Errorf("evidence source, scope, window or page mismatch")
	}
	key, err := k.key(e.KeyID)
	if err != nil {
		return err
	}
	data, err := canonicalEnvelope(e)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	signature, err := hex.DecodeString(e.MAC)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return fmt.Errorf("invalid evidence integrity")
	}
	return nil
}

func SignalMatch(s securityprofile.Signal, data []byte) (bool, bool) {
	if s.Kind == "stdout_nonempty" {
		return len(strings.TrimSpace(string(data))) > 0, true
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false, false
	}
	if s.Pointer != "" {
		for _, segment := range strings.Split(strings.TrimPrefix(s.Pointer, "/"), "/") {
			segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
			switch v := value.(type) {
			case map[string]any:
				var exists bool
				value, exists = v[segment]
				if !exists {
					return false, false
				}
			case []any:
				i, err := strconv.Atoi(segment)
				if err != nil || i < 0 || i >= len(v) || strconv.Itoa(i) != segment {
					return false, false
				}
				value = v[i]
			default:
				return false, false
			}
		}
	}
	switch s.Kind {
	case "json_equals":
		if value == nil {
			return false, false
		}
		return securityprofile.Digest(value) == securityprofile.Digest(s.Value), true
	case "json_array_nonempty":
		v, ok := value.([]any)
		return len(v) > 0, ok
	case "json_count_gt":
		v, ok := value.(float64)
		threshold, valid := securityprofile.Numeric(s.Value)
		return v > threshold, ok && valid
	}
	return false, false
}

func (s *Store) CommitPage(e Envelope, r Report, findings []Finding, manifests ...Manifest) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = reportGeneration(tx, r); err != nil {
		return err
	}
	var cursor int64
	err = tx.QueryRow("SELECT cursor FROM cursors WHERE deployment=? AND source=?", e.Deployment, e.Run+":"+e.Source).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cursor != e.Cursor {
		return ErrConflict
	}
	for i := range findings {
		finding := &findings[i]
		if finding.EvidenceID == "" {
			return fmt.Errorf("finding requires durable evidence reference")
		}
		var old []byte
		err = tx.QueryRow("SELECT body FROM findings WHERE deployment=? AND id=?", e.Deployment, finding.ID).Scan(&old)
		if err == nil {
			var prior Finding
			if err = json.Unmarshal(old, &prior); err != nil {
				return err
			}
			finding.FirstSeen = prior.FirstSeen
			finding.Occurrences = prior.Occurrences + 1
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		body, marshalErr := json.Marshal(finding)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec("INSERT INTO findings VALUES (?,?,?) ON CONFLICT(deployment,id) DO UPDATE SET body=excluded.body", e.Deployment, finding.ID, body); err != nil {
			return err
		}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	id := securityprofile.Digest(e)
	if _, err = tx.Exec("INSERT OR IGNORE INTO evidence VALUES (?,?,?,?)", e.Deployment, e.Run, id, data); err != nil {
		return err
	}
	r.Findings = append(r.Findings, findings...)
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE reports SET body=? WHERE deployment=? AND id=?", body, r.Deployment, r.ID); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO cursors VALUES (?,?,?) ON CONFLICT(deployment,source) DO UPDATE SET cursor=excluded.cursor", e.Deployment, e.Run+":"+e.Source, e.NextCursor); err != nil {
		return err
	}
	for _, manifest := range manifests {
		if manifest.Deployment != r.Deployment || manifest.files == nil {
			return fmt.Errorf("manifest scope mismatch")
		}
		var latest int64
		if err = tx.QueryRow("SELECT COALESCE(MAX(sequence),0) FROM manifests WHERE deployment=? AND scope=?", manifest.Deployment, manifest.Scope).Scan(&latest); err != nil {
			return err
		}
		if manifest.Sequence != latest+1 {
			return ErrConflict
		}
		body, marshalErr := json.Marshal(manifest)
		if marshalErr != nil {
			return marshalErr
		}
		files, marshalErr := json.Marshal(manifest.files)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec("INSERT INTO manifests VALUES (?,?,?,?,?)", manifest.Deployment, manifest.Scope, manifest.Sequence, body, files); err != nil {
			return err
		}
	}
	return tx.Commit()
}
