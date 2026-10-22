package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"

	DefaultSlug = "default"

	TypeDeployment        = "deployment"
	TypeCredential        = "credential"
	TypeSecret            = "secret"
	TypeBackupDestination = "backup_destination"
	TypeScheduledTask     = "scheduled_task"

	InvitationTTL = 7 * 24 * time.Hour
)

var (
	ErrNotFound          = errors.New("workspace not found")
	ErrSlugTaken         = errors.New("workspace slug is already taken")
	ErrInvalidSlug       = errors.New("workspace slug must use lowercase letters, digits and hyphens")
	ErrInvalidRole       = errors.New("workspace role must be owner, admin or member")
	ErrInvalidType       = errors.New("unknown resource type")
	ErrProtected         = errors.New("the default workspace cannot be deleted")
	ErrNotEmpty          = errors.New("workspace still owns resources")
	ErrLastOwner         = errors.New("a workspace needs at least one owner")
	ErrInvitationInvalid = errors.New("invitation is invalid or was revoked")
	ErrInvitationExpired = errors.New("invitation has expired")
	ErrInviteeMismatch   = errors.New("invitation was sent to someone else")
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var resourceTypes = map[string]bool{
	TypeDeployment:        true,
	TypeCredential:        true,
	TypeSecret:            true,
	TypeBackupDestination: true,
	TypeScheduledTask:     true,
}

type Workspace struct {
	ID        int64     `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Personal  bool      `json:"personal"`
	CreatedAt time.Time `json:"created_at"`
}

type Membership struct {
	Workspace
	Role string `json:"role"`
}

type Member struct {
	UserID    int64     `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type Invitation struct {
	ID        int64     `json:"id"`
	Invitee   string    `json:"invitee"`
	Role      string    `json:"role"`
	InvitedBy int64     `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Resource is anything a workspace can own. Ownership is stored by type and
// identifier, so a new kind of resource needs no schema change.
type Resource interface {
	ResourceType() string
	ResourceID() string
}

type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (r Ref) ResourceType() string { return r.Type }
func (r Ref) ResourceID() string   { return r.ID }

func ValidRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

func ValidResourceType(t string) bool {
	return resourceTypes[t]
}

// RoleRank orders roles so that a higher rank includes every lower one.
func RoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	}
	return 0
}

func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS workspaces (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		personal BOOLEAN NOT NULL DEFAULT FALSE,
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS workspace_members (
		workspace_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (workspace_id, user_id)
	);

	CREATE TABLE IF NOT EXISTS workspace_invitations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER NOT NULL,
		invitee TEXT NOT NULL,
		role TEXT NOT NULL,
		token_hash TEXT UNIQUE NOT NULL,
		invited_by INTEGER NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS workspace_resources (
		workspace_id INTEGER NOT NULL,
		resource_type TEXT NOT NULL,
		resource_id TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (resource_type, resource_id)
	);

	CREATE INDEX IF NOT EXISTS idx_workspace_members_user ON workspace_members(user_id);
	CREATE INDEX IF NOT EXISTS idx_workspace_resources_ws ON workspace_resources(workspace_id, resource_type);
	`)
	return err
}

const workspaceColumns = `id, slug, name, personal, created_at`

func scanWorkspace(row interface{ Scan(...any) error }) (*Workspace, error) {
	var w Workspace
	if err := row.Scan(&w.ID, &w.Slug, &w.Name, &w.Personal, &w.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

// Create adds a workspace with ownerID as its first owner. An empty slug is
// derived from the name, with a numeric suffix when the name is taken.
func (s *Store) Create(slug, name string, personal bool, ownerID int64) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	if slug == "" {
		base := Slugify(name)
		if base == "" {
			base = "workspace"
		}
		free, err := s.freeSlug(base)
		if err != nil {
			return nil, err
		}
		slug = free
	}
	if !slugPattern.MatchString(slug) {
		return nil, ErrInvalidSlug
	}
	if name == "" {
		name = slug
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := s.now().UTC()
	res, err := tx.Exec(`INSERT INTO workspaces (slug, name, personal, created_at) VALUES (?, ?, ?, ?)`, slug, name, personal, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrSlugTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if ownerID > 0 {
		if _, err := tx.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`, id, ownerID, RoleOwner, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Workspace{ID: id, Slug: slug, Name: name, Personal: personal, CreatedAt: now}, nil
}

func (s *Store) freeSlug(base string) (string, error) {
	for i := 1; i < 1000; i++ {
		candidate := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			if len(base)+len(suffix) > 63 {
				candidate = strings.TrimRight(base[:63-len(suffix)], "-") + suffix
			} else {
				candidate = base + suffix
			}
		}
		if _, err := s.Get(candidate); errors.Is(err, ErrNotFound) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", ErrSlugTaken
}

func (s *Store) Get(slug string) (*Workspace, error) {
	return scanWorkspace(s.db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces WHERE slug = ?`, slug))
}

func (s *Store) GetByID(id int64) (*Workspace, error) {
	return scanWorkspace(s.db.QueryRow(`SELECT `+workspaceColumns+` FROM workspaces WHERE id = ?`, id))
}

func (s *Store) List() ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT ` + workspaceColumns + ` FROM workspaces ORDER BY personal, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Store) ListForUser(userID int64) ([]Membership, error) {
	rows, err := s.db.Query(`
		SELECT w.id, w.slug, w.name, w.personal, w.created_at, m.role
		FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = ? ORDER BY w.personal DESC, w.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.Personal, &m.CreatedAt, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) PersonalFor(userID int64) (*Workspace, error) {
	return scanWorkspace(s.db.QueryRow(`
		SELECT w.id, w.slug, w.name, w.personal, w.created_at
		FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = ? AND m.role = ? AND w.personal = TRUE
		ORDER BY w.id LIMIT 1`, userID, RoleOwner))
}

func (s *Store) Rename(slug, name string) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("workspace name is required")
	}
	res, err := s.db.Exec(`UPDATE workspaces SET name = ? WHERE slug = ?`, name, slug)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(slug)
}

// Delete removes an empty workspace with its members and invitations.
func (s *Store) Delete(slug string) error {
	if slug == DefaultSlug {
		return ErrProtected
	}
	w, err := s.Get(slug)
	if err != nil {
		return err
	}
	var owned int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workspace_resources WHERE workspace_id = ?`, w.ID).Scan(&owned); err != nil {
		return err
	}
	if owned > 0 {
		return ErrNotEmpty
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM workspace_members WHERE workspace_id = ?`,
		`DELETE FROM workspace_invitations WHERE workspace_id = ?`,
		`DELETE FROM workspaces WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, w.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Role(workspaceID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Store) Members(workspaceID int64) ([]Member, error) {
	rows, err := s.db.Query(`SELECT user_id, role, created_at FROM workspace_members WHERE workspace_id = ? ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMember adds a member or changes their role. Demoting the last owner is
// refused.
func (s *Store) SetMember(workspaceID, userID int64, role string) error {
	if !ValidRole(role) {
		return ErrInvalidRole
	}
	current, err := s.Role(workspaceID, userID)
	if err != nil {
		return err
	}
	if current == RoleOwner && role != RoleOwner {
		if err := s.ensureAnotherOwner(workspaceID, userID); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role`,
		workspaceID, userID, role, s.now().UTC())
	return err
}

func (s *Store) RemoveMember(workspaceID, userID int64) error {
	current, err := s.Role(workspaceID, userID)
	if err != nil {
		return err
	}
	if current == "" {
		return ErrNotFound
	}
	if current == RoleOwner {
		if err := s.ensureAnotherOwner(workspaceID, userID); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	return err
}

func (s *Store) ensureAnotherOwner(workspaceID, userID int64) error {
	var others int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND role = ? AND user_id != ?`, workspaceID, RoleOwner, userID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastOwner
	}
	return nil
}

// RemoveUser drops every membership of a deleted user. Their personal
// workspace is removed and anything it owned moves to the default workspace.
func (s *Store) RemoveUser(userID int64) error {
	personal, err := s.PersonalFor(userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if personal != nil {
		def, err := s.Get(DefaultSlug)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE workspace_resources SET workspace_id = ? WHERE workspace_id = ?`, def.ID, personal.ID); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM workspace_members WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if personal != nil {
		if _, err := s.db.Exec(`DELETE FROM workspace_invitations WHERE workspace_id = ?`, personal.ID); err != nil {
			return err
		}
		if _, err := s.db.Exec(`DELETE FROM workspaces WHERE id = ?`, personal.ID); err != nil {
			return err
		}
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Invite records an invitation and returns the token to hand to the invitee.
// Only the token's hash is stored.
func (s *Store) Invite(workspaceID int64, invitee, role string, invitedBy int64, ttl time.Duration) (*Invitation, string, error) {
	invitee = strings.TrimSpace(invitee)
	if invitee == "" {
		return nil, "", errors.New("invitee is required")
	}
	if !ValidRole(role) {
		return nil, "", ErrInvalidRole
	}
	if ttl <= 0 {
		ttl = InvitationTTL
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := hex.EncodeToString(raw)
	now := s.now().UTC()
	inv := &Invitation{Invitee: invitee, Role: role, InvitedBy: invitedBy, ExpiresAt: now.Add(ttl), CreatedAt: now}
	res, err := s.db.Exec(`
		INSERT INTO workspace_invitations (workspace_id, invitee, role, token_hash, invited_by, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, invitee, role, hashToken(token), invitedBy, inv.ExpiresAt, now)
	if err != nil {
		return nil, "", err
	}
	inv.ID, _ = res.LastInsertId()
	return inv, token, nil
}

func (s *Store) Invitations(workspaceID int64) ([]Invitation, error) {
	rows, err := s.db.Query(`
		SELECT id, invitee, role, invited_by, expires_at, created_at
		FROM workspace_invitations WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(&inv.ID, &inv.Invitee, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Store) RevokeInvitation(workspaceID, id int64) error {
	res, err := s.db.Exec(`DELETE FROM workspace_invitations WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Accept turns an invitation into a membership. The accepting user must be
// the invitee, matched on username or email without regard to case. A role
// the user already holds is never lowered.
func (s *Store) Accept(token string, userID int64, identities ...string) (*Membership, error) {
	var (
		invID       int64
		workspaceID int64
		invitee     string
		role        string
		expiresAt   time.Time
	)
	err := s.db.QueryRow(`
		SELECT id, workspace_id, invitee, role, expires_at FROM workspace_invitations WHERE token_hash = ?`,
		hashToken(strings.TrimSpace(token))).Scan(&invID, &workspaceID, &invitee, &role, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvitationInvalid
	}
	if err != nil {
		return nil, err
	}
	if s.now().After(expiresAt) {
		return nil, ErrInvitationExpired
	}
	matched := false
	for _, id := range identities {
		if id != "" && strings.EqualFold(id, invitee) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, ErrInviteeMismatch
	}
	current, err := s.Role(workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if RoleRank(current) < RoleRank(role) {
		if err := s.SetMember(workspaceID, userID, role); err != nil {
			return nil, err
		}
		current = role
	}
	if _, err := s.db.Exec(`DELETE FROM workspace_invitations WHERE id = ?`, invID); err != nil {
		return nil, err
	}
	w, err := s.GetByID(workspaceID)
	if err != nil {
		return nil, err
	}
	return &Membership{Workspace: *w, Role: current}, nil
}

// Assign makes a workspace the owner of a resource, moving it if another
// workspace owned it.
func (s *Store) Assign(workspaceID int64, r Resource) error {
	if !ValidResourceType(r.ResourceType()) || r.ResourceID() == "" {
		return ErrInvalidType
	}
	_, err := s.db.Exec(`
		INSERT INTO workspace_resources (workspace_id, resource_type, resource_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(resource_type, resource_id) DO UPDATE SET workspace_id = excluded.workspace_id`,
		workspaceID, r.ResourceType(), r.ResourceID(), s.now().UTC())
	return err
}

func (s *Store) Release(r Resource) error {
	_, err := s.db.Exec(`DELETE FROM workspace_resources WHERE resource_type = ? AND resource_id = ?`, r.ResourceType(), r.ResourceID())
	return err
}

// Owner returns the workspace that owns a resource, or ErrNotFound.
func (s *Store) Owner(r Resource) (*Workspace, error) {
	return scanWorkspace(s.db.QueryRow(`
		SELECT w.id, w.slug, w.name, w.personal, w.created_at
		FROM workspaces w JOIN workspace_resources r ON r.workspace_id = w.id
		WHERE r.resource_type = ? AND r.resource_id = ?`, r.ResourceType(), r.ResourceID()))
}

// Owners maps every owned resource of one type to its workspace slug.
func (s *Store) Owners(resourceType string) (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT r.resource_id, w.slug FROM workspace_resources r JOIN workspaces w ON w.id = r.workspace_id
		WHERE r.resource_type = ?`, resourceType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		out[id] = slug
	}
	return out, rows.Err()
}

func (s *Store) Resources(workspaceID int64, resourceType string) ([]string, error) {
	query := `SELECT resource_type, resource_id FROM workspace_resources WHERE workspace_id = ?`
	args := []any{workspaceID}
	if resourceType != "" {
		query += ` AND resource_type = ?`
		args = append(args, resourceType)
	}
	rows, err := s.db.Query(query+` ORDER BY resource_type, resource_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t, id string
		if err := rows.Scan(&t, &id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) ResourceRefs(workspaceID int64) ([]Ref, error) {
	rows, err := s.db.Query(`SELECT resource_type, resource_id FROM workspace_resources WHERE workspace_id = ? ORDER BY resource_type, resource_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ref{}
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.Type, &r.ID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResourceRoles returns, for one resource type, the highest workspace role
// the user holds over each resource through membership.
func (s *Store) ResourceRoles(userID int64, resourceType string) (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT r.resource_id, m.role FROM workspace_resources r
		JOIN workspace_members m ON m.workspace_id = r.workspace_id
		WHERE m.user_id = ? AND r.resource_type = ?`, userID, resourceType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		if RoleRank(role) > RoleRank(out[id]) {
			out[id] = role
		}
	}
	return out, rows.Err()
}

// EnsureDefault creates the default workspace on first use, owned by the
// given users.
func (s *Store) EnsureDefault(ownerIDs []int64) (*Workspace, error) {
	if w, err := s.Get(DefaultSlug); err == nil {
		return w, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var first int64
	if len(ownerIDs) > 0 {
		first = ownerIDs[0]
	}
	w, err := s.Create(DefaultSlug, "Default", false, first)
	if err != nil {
		return nil, err
	}
	for _, id := range ownerIDs[min(1, len(ownerIDs)):] {
		if err := s.SetMember(w.ID, id, RoleOwner); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// EnsurePersonal gives a user their own workspace if they have none.
func (s *Store) EnsurePersonal(userID int64, username string) (*Workspace, error) {
	if w, err := s.PersonalFor(userID); err == nil {
		return w, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	base := Slugify(username)
	if base == "" || base == DefaultSlug {
		base = "personal"
	}
	slug, err := s.freeSlug(base)
	if err != nil {
		return nil, err
	}
	return s.Create(slug, username+"'s workspace", true, userID)
}
