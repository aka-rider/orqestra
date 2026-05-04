package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var adjectives = []string{
	"amber", "azure", "blue", "bronze", "brown", "cobalt", "crimson", "cyan",
	"dark", "ebony", "emerald", "fuchsia", "gold", "golden", "gray", "green",
	"indigo", "ivory", "jade", "khaki", "lemon", "magenta", "maroon", "navy",
	"olive", "orange", "peach", "pink", "plum", "purple", "quartz", "red",
	"ruby", "sapphire", "scarlet", "silver", "teal", "umber", "violet", "white",
	"yellow", "agile", "bold", "brave", "bright", "calm", "clever", "cool",
	"eager", "fast", "fierce", "firm", "fleet", "grand", "great", "hardy",
	"heroic", "keen", "kind", "lucid", "noble", "proud", "quick", "quiet",
	"rapid", "sharp", "smart", "solid", "steady", "swift", "vast", "warm",
	"wise", "witty", "zealous", "active", "astute", "awake", "busy", "crisp",
	"dynamic", "energetic", "expert", "fluid", "flying", "nimble", "nimble",
	"prime", "ready", "robust", "secure", "snappy", "spry", "strong", "sturdy",
}

var nouns = []string{
	"serpent", "eagle", "tiger", "wolf", "bear", "falcon", "lion", "panther",
	"shark", "hawk", "raven", "fox", "dragon", "griffin", "phoenix", "viper",
	"cobra", "phantom", "badger", "bison", "boar", "cat", "cheetah", "crab",
	"crane", "crow", "deer", "dingo", "dog", "dolphin", "dove", "elk",
	"finch", "fish", "frog", "gecko", "goat", "goose", "hare", "heron",
	"horse", "hound", "husky", "iguana", "jackal", "jaguar", "kite", "koala",
	"lemur", "leopard", "llama", "lynx", "macaque", "macaw", "marlin", "mink",
	"moose", "mouse", "mule", "newt", "ostrich", "otter", "owl", "ox",
	"panda", "parrot", "pelican", "penguin", "puma", "puma", "puma", "puma",
	"quail", "rabbit", "ram", "rat", "rhino", "robin", "seal", "shark",
	"sheep", "skunk", "sloth", "snake", "snipe", "squid", "stork", "swan",
	"tapir", "toad", "trout", "turtle", "walrus", "weasel", "whale", "yak",
}

// GenerateSessionName generates a unique session name using the format:
// YYYY-MM-DDTHH-MM-SS-adjective-noun-hexhash
func GenerateSessionName() (string, error) {
	now := time.Now().UTC()
	timestamp := now.Format("2006-01-02T15-04-05")

	hashBytes := make([]byte, 4)
	if _, err := rand.Read(hashBytes); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	hashPrefix := hex.EncodeToString(hashBytes) // 8 hex chars

	// Select random indices safely using crypto/rand
	adjIdx, err := cryptoRandInt(len(adjectives))
	if err != nil {
		return "", err
	}

	nounIdx, err := cryptoRandInt(len(nouns))
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s-%s-%s-%s",
		timestamp,
		adjectives[adjIdx],
		nouns[nounIdx],
		hashPrefix,
	)

	return name, nil
}

func cryptoRandInt(max int) (int, error) {
	if max <= 0 {
		return 0, errors.New("max must be > 0")
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return 0, fmt.Errorf("crypto rand read: %w", err)
	}
	
	// Convert bytes to uint32
	v := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return int(v % uint32(max)), nil
}

// CreateSessionDir creates a unique directory within basePath.
func CreateSessionDir(basePath string) (string, error) {
	for i := 0; i < 5; i++ {
		name, err := GenerateSessionName()
		if err != nil {
			return "", err
		}

		sessionPath := filepath.Join(basePath, name)
		
		// Ensure basePath exists before trying to create leaf
		if err := os.MkdirAll(basePath, 0755); err != nil {
			return "", fmt.Errorf("create base path %q: %w", basePath, err)
		}

		err = os.Mkdir(sessionPath, 0755)
		if err != nil {
			if os.IsExist(err) {
				continue // retry
			}
			return "", fmt.Errorf("create session dir %q: %w", sessionPath, err)
		}

		return sessionPath, nil
	}

	return "", errors.New("failed to generate unique session directory after 5 attempts")
}
