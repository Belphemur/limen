package signup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Zitadel organization names are an internal slug, not a user-facing
// label: Limen stores the user's display name verbatim in
// tenants.name and treats the Zitadel org name as opaque. The slug
// must (a) be deterministically derived from the display name so
// support can still find an org from its tenant, (b) survive
// Zitadel's uniqueness constraint by appending random safe-word
// suffixes on collision, and (c) never overflow Zitadel's 200-char
// name limit.

const (
	// slugBaseMaxLen caps the base slug so that the worst-case suffix
	// ("-<word>-<word>" with longest words in safeWords) still fits
	// well under Zitadel's 200-char organisation-name limit.
	slugBaseMaxLen = 120

	// slugMaxCollisionRetries bounds the safe-word-suffix retry loop
	// in CompleteSignup. With a wordlist of len(safeWords) ≈ 96 the
	// 2-word combination space is ~9k; five collisions in a row would
	// imply somebody is deliberately squatting.
	slugMaxCollisionRetries = 5
)

// safeWords is a small hand-curated list of common, neutral English
// words used to disambiguate colliding Zitadel org slugs. Picked for:
// short length, no offensive or trademark connotations, easy to
// read out loud over a support call. Keep entries lowercase ASCII.
var safeWords = []string{
	"acorn", "amber", "apple", "arrow", "aspen", "azure",
	"basil", "beach", "beryl", "birch", "blaze", "bloom", "brook",
	"cedar", "cliff", "cloud", "clove", "comet", "coral", "cove", "crisp", "crown",
	"daisy", "delta", "drift", "dune",
	"ember", "fable", "falcon", "fern", "flint", "frost",
	"glade", "glow", "grove",
	"hazel", "heath", "horizon",
	"indigo", "ivory",
	"jasper",
	"kelp",
	"lake", "linen",
	"maple", "marble", "meadow", "mesa", "mint", "misty", "moss",
	"nectar", "north",
	"oak", "ocean", "olive", "orchid", "otter",
	"pearl", "pine", "plum", "prairie",
	"quartz", "quiet",
	"raven", "reed", "ridge", "rose",
	"sage", "shore", "silk", "silver", "slate", "solace", "south", "spruce", "stone", "sunny",
	"tide", "topaz", "trail",
	"valley", "velvet", "violet",
	"willow", "wave",
}

// slugify normalises a display name into a lowercase ASCII slug
// suitable for use as a Zitadel organisation name. Diacritics are
// stripped, non-alphanumeric runes collapse to single hyphens, and
// the result is bounded by slugBaseMaxLen. An empty result is
// replaced with "tenant" so we never hand Zitadel a name that would
// rely entirely on the random suffix for content.
func slugify(display string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, display)
	if err != nil {
		folded = display
	}
	var b strings.Builder
	b.Grow(len(folded))
	lastHyphen := true
	for _, r := range strings.ToLower(folded) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "tenant"
	}
	if len(out) > slugBaseMaxLen {
		out = strings.TrimRight(out[:slugBaseMaxLen], "-")
	}
	return out
}

// orgSlugCandidate returns the slug Limen should try as a Zitadel
// organisation name on retry attempt `attempt` (0-indexed). The
// first attempt uses the bare slug; subsequent attempts append a
// fresh "-<word>-<word>" suffix drawn from safeWords with
// cryptographically-random selection. A final fallback after
// slugMaxCollisionRetries uses 8 hex characters to guarantee
// statistical termination even if the safe-word pool is exhausted by
// repeated collisions.
func orgSlugCandidate(base string, attempt int) (string, error) {
	if attempt <= 0 {
		return base, nil
	}
	if attempt > slugMaxCollisionRetries {
		suffix, err := randomHexSuffix(4)
		if err != nil {
			return "", err
		}
		return base + "-" + suffix, nil
	}
	w1, err := pickSafeWord()
	if err != nil {
		return "", err
	}
	w2, err := pickSafeWord()
	if err != nil {
		return "", err
	}
	return base + "-" + w1 + "-" + w2, nil
}

func pickSafeWord() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(safeWords))))
	if err != nil {
		return "", fmt.Errorf("signup: pick safe word: %w", err)
	}
	return safeWords[n.Int64()], nil
}

func randomHexSuffix(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("signup: random hex suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
