package workspace

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Web":     "acme-web",
		"  ops__team ": "ops-team",
		"Café 2":       "caf-2",
		"---":          "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateDerivesAFreeSlug(t *testing.T) {
	s := newStore(t)
	a, err := s.Create("", "Acme Web", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create("", "Acme web", false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if a.Slug != "acme-web" || b.Slug != "acme-web-2" {
		t.Fatalf("slugs = %q, %q", a.Slug, b.Slug)
	}
	if _, err := s.Create("acme-web", "x", false, 1); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("duplicate slug error = %v", err)
	}
	if _, err := s.Create("Bad Slug", "x", false, 1); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("invalid slug error = %v", err)
	}
	if role, _ := s.Role(a.ID, 1); role != RoleOwner {
		t.Fatalf("creator role = %q", role)
	}
}

func TestWorkspaceKeepsAnOwner(t *testing.T) {
	s := newStore(t)
	w, _ := s.Create("team", "Team", false, 1)
	if err := s.SetMember(w.ID, 1, RoleMember); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting the last owner: %v", err)
	}
	if err := s.RemoveMember(w.ID, 1); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last owner: %v", err)
	}
	if err := s.SetMember(w.ID, 2, RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(w.ID, 1); err != nil {
		t.Fatalf("removing one of two owners: %v", err)
	}
}

func TestOwnershipIsPolymorphic(t *testing.T) {
	s := newStore(t)
	team, _ := s.Create("team", "Team", false, 1)
	other, _ := s.Create("other", "Other", false, 2)

	for _, r := range []Ref{{TypeDeployment, "shop"}, {TypeCredential, "s3-main"}, {TypeSecret, "shop/DB_PASSWORD"}} {
		if err := s.Assign(team.ID, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Assign(team.ID, Ref{"planet", "x"}); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("unknown type error = %v", err)
	}
	if ids, _ := s.Resources(team.ID, TypeDeployment); len(ids) != 1 || ids[0] != "shop" {
		t.Fatalf("deployments = %v", ids)
	}
	if refs, _ := s.ResourceRefs(team.ID); len(refs) != 3 {
		t.Fatalf("refs = %v", refs)
	}

	if err := s.Assign(other.ID, Ref{TypeDeployment, "shop"}); err != nil {
		t.Fatal(err)
	}
	owner, err := s.Owner(Ref{TypeDeployment, "shop"})
	if err != nil || owner.Slug != "other" {
		t.Fatalf("owner after move = %v, %v", owner, err)
	}
	if err := s.Delete("other"); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("deleting a workspace that owns resources: %v", err)
	}
	if err := s.Release(Ref{TypeDeployment, "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("other"); err != nil {
		t.Fatalf("deleting an empty workspace: %v", err)
	}
}

func TestResourceRolesTakeTheHighestMembership(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create("a", "A", false, 9)
	b, _ := s.Create("b", "B", false, 9)
	_ = s.SetMember(a.ID, 1, RoleMember)
	_ = s.SetMember(b.ID, 1, RoleAdmin)
	_ = s.Assign(a.ID, Ref{TypeDeployment, "api"})
	_ = s.Assign(b.ID, Ref{TypeDeployment, "web"})
	_ = s.Assign(b.ID, Ref{TypeCredential, "api"})

	roles, err := s.ResourceRoles(1, TypeDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if roles["api"] != RoleMember || roles["web"] != RoleAdmin || len(roles) != 2 {
		t.Fatalf("roles = %v", roles)
	}
}

func TestInvitationAcceptance(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	w, _ := s.Create("team", "Team", false, 1)

	_, token, err := s.Invite(w.ID, "Dev@Example.com", RoleAdmin, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(token, 2, "someone", "other@example.com"); !errors.Is(err, ErrInviteeMismatch) {
		t.Fatalf("wrong invitee error = %v", err)
	}
	m, err := s.Accept(token, 2, "dev", "dev@example.com")
	if err != nil || m.Role != RoleAdmin || m.Slug != "team" {
		t.Fatalf("accept = %+v, %v", m, err)
	}
	if _, err := s.Accept(token, 2, "dev"); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("reusing a token: %v", err)
	}

	_, late, _ := s.Invite(w.ID, "late", RoleMember, 1, time.Hour)
	now = now.Add(2 * time.Hour)
	if _, err := s.Accept(late, 3, "late"); !errors.Is(err, ErrInvitationExpired) {
		t.Fatalf("expired token: %v", err)
	}

	_, lower, _ := s.Invite(w.ID, "dev", RoleMember, 1, 0)
	if m, _ := s.Accept(lower, 2, "dev"); m.Role != RoleAdmin {
		t.Fatalf("a lower invitation changed the role to %q", m.Role)
	}
}

func TestPersonalWorkspacesFoldIntoDefault(t *testing.T) {
	s := newStore(t)
	def, _ := s.EnsureDefault([]int64{1})
	personal, err := s.Create("dev", "Dev's workspace", true, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Assign(personal.ID, Ref{TypeDeployment, "blog"})
	if _, _, err := s.Invite(personal.ID, "ops", RoleMember, 2, 0); err != nil {
		t.Fatal(err)
	}

	if err := s.FoldPersonal(); err != nil {
		t.Fatal(err)
	}
	owner, err := s.Owner(Ref{TypeDeployment, "blog"})
	if err != nil || owner.ID != def.ID {
		t.Fatalf("owner = %v, %v", owner, err)
	}
	if _, err := s.Get("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal workspace still exists: %v", err)
	}
	if err := s.FoldPersonal(); err != nil {
		t.Fatalf("folding twice: %v", err)
	}
	if err := s.Delete(DefaultSlug); !errors.Is(err, ErrProtected) {
		t.Fatalf("deleting default: %v", err)
	}
}
