package subdomain

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

var adjectives = []string{
	"swift", "bright", "calm", "bold", "cool", "eager", "fair", "glad",
	"happy", "keen", "lively", "merry", "neat", "proud", "quick", "rare",
	"sharp", "sunny", "warm", "wise", "agile", "brave", "clever", "daring",
	"fleet", "golden", "humble", "jolly", "kind", "lucky", "noble", "polite",
	"quiet", "royal", "steady", "tender", "upbeat", "vivid", "witty", "zesty",
	"azure", "coral", "crimson", "dusty", "emerald", "frosty", "gentle", "hazy",
}

var nouns = []string{
	"river", "cloud", "leaf", "stone", "wind", "flame", "wave", "star",
	"moon", "sun", "tree", "bird", "lake", "peak", "vale", "grove",
	"brook", "meadow", "shore", "ridge", "frost", "dawn", "dusk", "mist",
	"rain", "snow", "spring", "forest", "ocean", "island", "canyon", "delta",
	"harbor", "lagoon", "marsh", "oasis", "plain", "prairie", "reef", "summit",
	"tundra", "valley", "willow", "aurora", "breeze", "crystal", "ember", "glacier",
}

type Generator struct {
	style string
}

func NewGenerator(style string) *Generator {
	if style == "" {
		style = "words"
	}
	return &Generator{style: style}
}

func (g *Generator) Generate() string {
	switch g.style {
	case "words":
		return g.generateWords()
	case "hex":
		return g.generateHex()
	case "short":
		return g.generateShort()
	default:
		return g.generateWords()
	}
}

func (g *Generator) generateWords() string {
	adj := randomChoice(adjectives)
	noun := randomChoice(nouns)
	num := randomNumber(100, 999)
	return fmt.Sprintf("%s-%s-%d", adj, noun, num)
}

func (g *Generator) generateHex() string {
	bytes := make([]byte, 4)
	_, _ = rand.Read(bytes)
	return fmt.Sprintf("%x", bytes)
}

func (g *Generator) generateShort() string {
	adj := randomChoice(adjectives)
	noun := randomChoice(nouns)
	return fmt.Sprintf("%s-%s", adj[:3], noun[:3])
}

func (g *Generator) GenerateForDomain(baseDomain string) string {
	subdomain := g.Generate()
	if baseDomain == "" {
		return subdomain
	}
	return fmt.Sprintf("%s.%s", subdomain, baseDomain)
}

func randomChoice(list []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	return list[n.Int64()]
}

func randomNumber(min, max int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	return int(n.Int64()) + min
}

func IsValidSubdomain(subdomain string) bool {
	if len(subdomain) == 0 || len(subdomain) > 63 {
		return false
	}

	subdomain = strings.ToLower(subdomain)

	for _, c := range subdomain {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}

	if subdomain[0] == '-' || subdomain[len(subdomain)-1] == '-' {
		return false
	}

	return true
}
