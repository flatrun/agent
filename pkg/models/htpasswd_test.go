package models

import (
	"strings"
	"testing"
)

// Expected values were produced by `openssl passwd -6 -salt SALT PASSWORD`.
func TestSHA512CryptMatchesOpenSSL(t *testing.T) {
	cases := []struct{ password, salt, want string }{
		{"s3cret", "saltsalt", "$6$saltsalt$As4wrv0kZlfch1du9WeH7qhskyLriQWySXrZzynnvi46nFnNxjdpl6ksRegrrKexvhIa/Iny8S8uF3fVWTMuC1"},
		{"correct-horse-battery", "abcdefghijklmnop", "$6$abcdefghijklmnop$fDChZ3OXFbbxjJ8EMMF13NX.O/qyB0VZ4H3fzQJReXyuwv56vYY3HPzj.ixPeI0SHD14XO9gGy0up7u4mwx2R."},
		{"p", "salt", "$6$salt$BXyrstkgSyErowvJGOokNhh9f/aSAl7AWKdxdHfngLkMMVQ2ynbYGLpvtENMNa4bjrDa4JmRjGRd/0nlL9Tj31"},
		{"a-very-long-password-that-is-more-than-sixty-four-bytes-long-for-sure-yes", "x1Y2z3", "$6$x1Y2z3$YlC9Gx2E9CC2YIXgMrofGrpN4SJWgip/Fxge8XSU46m4mGp98i96LckmbG61NbwQIvXQXbyjY8PMTs8raAI480"},
	}
	for _, c := range cases {
		if got := sha512Crypt(c.password, c.salt); got != c.want {
			t.Errorf("sha512Crypt(%q, %q) = %s, want %s", c.password, c.salt, got, c.want)
		}
	}
}

func TestHashBasicAuthPasswordUsesFreshSalt(t *testing.T) {
	a, err := HashBasicAuthPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := HashBasicAuthPassword("s3cret")
	if a == b || !strings.HasPrefix(a, "$6$") {
		t.Fatalf("hashes %q and %q must be salted SHA-512 crypt", a, b)
	}
	salt := strings.Split(a, "$")[2]
	if sha512Crypt("s3cret", salt) != a {
		t.Fatal("hash does not verify against its own salt")
	}
}
