package tui

import (
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// displayTaskName transforms only rendered titles; task identity and cached data
// stay unchanged. The name seeds the words, while the stable task ID supplies the suffix.
func (model Model) displayTaskName(name, id string) string {
	if !model.maskNames {
		return name
	}
	adjectives := [...]string{
		"Quiet", "Amber", "Silver", "Gentle", "Calm", "Sunny", "Misty", "Soft",
		"Warm", "Cool", "Bright", "Clear", "Fresh", "Green", "Blue", "Golden",
		"Little", "Still", "Light", "Happy", "Cozy", "Mellow", "Kind", "Sweet",
		"Early", "Late", "Pale", "Rosy", "Snowy", "Breezy", "Mild", "Dewy",
	}
	nouns := [...]string{
		"Meadow", "Harbor", "Grove", "River", "Brook", "Hill", "Cloud", "Lake",
		"Willow", "Cedar", "Maple", "Pine", "Birch", "Fern", "Moss", "Pebble",
		"Garden", "Valley", "Forest", "Field", "Shore", "Island", "Leaf", "Petal",
		"Dawn", "Dusk", "Breeze", "Rain", "Snow", "Spring", "Summer", "Autumn",
	}
	seed := fnv.New64a()
	_, _ = seed.Write([]byte(name))
	random := rand.New(rand.NewPCG(seed.Sum64(), 0))
	// The vocabulary averages about five letters per word, plus a separating space.
	wordCount := max(2, (utf8.RuneCountInString(name)+3)/6)
	words := make([]string, wordCount)
	for i := range words {
		if i%2 == 0 {
			words[i] = adjectives[random.IntN(len(adjectives))]
		} else {
			words[i] = nouns[random.IntN(len(nouns))]
		}
	}
	alias := strings.Join(words, " ")
	if id != "" {
		shortID := []rune(id)
		alias += " - " + strings.ToUpper(string(shortID[max(0, len(shortID)-3):]))
	}
	return alias
}
