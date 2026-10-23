package envref

import (
	"errors"
	"strings"
	"testing"
)

type mapSource map[string]map[string]string

func (m mapSource) Raw(d string) (map[string]string, error) {
	vars, ok := m[d]
	if !ok {
		return nil, ErrUnknownDeployment
	}
	return vars, nil
}

func (m mapSource) Host(d string) (string, error) {
	if _, ok := m[d]; !ok {
		return "", ErrUnknownDeployment
	}
	return d, nil
}

func TestResolve(t *testing.T) {
	src := mapSource{
		"db":  {"PASSWORD": "pw", "URL": "postgres://app:${{ db.PASSWORD }}@${{db.FLATRUN_HOST}}:5432/app"},
		"api": {},
	}
	got, err := Resolver{Source: src}.Resolve("api", map[string]string{
		"DATABASE_URL": "${{ db.URL }}",
		"PLAIN":        "value",
		"SELF":         "${{ api.PLAIN }}-x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["DATABASE_URL"] != "postgres://app:pw@db:5432/app" {
		t.Errorf("DATABASE_URL = %q", got["DATABASE_URL"])
	}
	if got["PLAIN"] != "value" || got["SELF"] != "value-x" {
		t.Errorf("got = %v", got)
	}
}

func TestResolveErrors(t *testing.T) {
	src := mapSource{
		"a": {"X": "${{ b.Y }}"},
		"b": {"Y": "${{ a.X }}"},
	}
	cases := map[string]string{
		"${{ a.X }}":       "cycle",
		"${{ missing.X }}": "not found",
		"${{ b.NOPE }}":    "has no variable NOPE",
	}
	for value, want := range cases {
		_, err := Resolver{Source: src}.Resolve("c", map[string]string{"K": value})
		var refErr *Error
		if !errors.As(err, &refErr) || refErr.Key != "K" || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) err = %v, want %q", value, err, want)
		}
	}
}

func TestHasReference(t *testing.T) {
	if !HasReference("x ${{ api.KEY }} y") || HasReference("${API_KEY}") || HasReference("plain") {
		t.Error("HasReference misclassified a value")
	}
}
