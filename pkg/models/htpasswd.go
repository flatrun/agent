package models

import (
	"crypto/rand"
	"crypto/sha512"
	"strings"
)

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// HashBasicAuthPassword returns a SHA-512 crypt ("$6$") hash for an htpasswd
// file. nginx checks htpasswd entries with the system crypt(), and the musl
// crypt() in Alpine-based nginx images does not accept bcrypt, so bcrypt
// hashes would reject every password there.
func HashBasicAuthPassword(password string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	for i, b := range raw {
		salt[i] = cryptAlphabet[int(b)%len(cryptAlphabet)]
	}
	return sha512Crypt(password, string(salt)), nil
}

// sha512Crypt implements SHA-crypt with SHA-512 and the default 5000
// rounds, as specified by Ulrich Drepper and used by glibc and musl.
func sha512Crypt(password, salt string) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	p, s := []byte(password), []byte(salt)

	alt := sha512.New()
	alt.Write(p)
	alt.Write(s)
	alt.Write(p)
	altSum := alt.Sum(nil)

	a := sha512.New()
	a.Write(p)
	a.Write(s)
	for n := len(p); n > 0; n -= 64 {
		a.Write(altSum[:min(n, 64)])
	}
	for n := len(p); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(altSum)
		} else {
			a.Write(p)
		}
	}
	sum := a.Sum(nil)

	dp := sha512.New()
	for range p {
		dp.Write(p)
	}
	pSeq := repeatTo(dp.Sum(nil), len(p))

	ds := sha512.New()
	for i := 0; i < 16+int(sum[0]); i++ {
		ds.Write(s)
	}
	sSeq := repeatTo(ds.Sum(nil), len(s))

	for i := 0; i < 5000; i++ {
		c := sha512.New()
		if i&1 != 0 {
			c.Write(pSeq)
		} else {
			c.Write(sum)
		}
		if i%3 != 0 {
			c.Write(sSeq)
		}
		if i%7 != 0 {
			c.Write(pSeq)
		}
		if i&1 != 0 {
			c.Write(sum)
		} else {
			c.Write(pSeq)
		}
		sum = c.Sum(nil)
	}

	order := [][3]int{
		{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
	}
	var out strings.Builder
	out.WriteString("$6$" + salt + "$")
	encode := func(w uint, n int) {
		for ; n > 0; n-- {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	for _, o := range order {
		encode(uint(sum[o[0]])<<16|uint(sum[o[1]])<<8|uint(sum[o[2]]), 4)
	}
	encode(uint(sum[63]), 2)
	return out.String()
}

func repeatTo(block []byte, length int) []byte {
	out := make([]byte, 0, length)
	for len(out) < length {
		out = append(out, block[:min(len(block), length-len(out))]...)
	}
	return out
}
