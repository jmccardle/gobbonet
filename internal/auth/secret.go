package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Three secret formats are accepted.
//
// Legacy — "salt:hash", lowercase hex, hash = SHA-256(salt + password). This is
// what launch.bat's :setup_password block wrote and what fileserver.ps1 checked.
// It is a single round of SHA-256, which a consumer GPU brute-forces at billions
// of guesses per second; it exists here only so an existing install keeps
// working across the upgrade.
//
// PBKDF2 — "pbkdf2-sha256:<iterations>:<salt-hex>:<hash-hex>", what upstream
// launch.bat writes since ElodineOfficial/gobbonet#6. See pbkdf2Re below.
//
// Current — Argon2id in PHC string format. Memory-hard, so the same GPU buys
// the attacker almost nothing.
//
// A legacy secret that verifies is rewritten as Argon2id on the spot, so users
// migrate by logging in once and never see a forced re-auth.
var legacyRe = regexp.MustCompile(`^([0-9a-fA-F]+):([0-9a-fA-F]+)$`)

// pbkdf2Re matches what launch.bat's :setup_password writes:
//
//	pbkdf2-sha256:210000:<32 hex chars>:<64 hex chars>
//
// Field order is kdf, iterations, salt, hash. Iterations are plain decimal.
// Salt and hash are lowercase hex of raw bytes — the launcher hexes the 16
// random salt BYTES for storage but feeds Rfc2898DeriveBytes the bytes, not the
// hex text. The legacy format above does the opposite: it hashes the hex text.
// Getting that backwards produces a verifier that never matches, so decode the
// salt before use.
//
// The iteration count is bounded to nine digits so strconv.Atoi cannot be handed
// something that overflows a 32-bit int, and so a hand-edited config cannot ask
// us to burn a year of CPU on one login.
var pbkdf2Re = regexp.MustCompile(`^pbkdf2-sha256:([0-9]{1,9}):([0-9a-fA-F]+):([0-9a-fA-F]+)$`)

const pbkdf2Prefix = "pbkdf2-sha256:"

type pbkdf2Secret struct {
	iterations int
	salt       []byte
	want       []byte
}

// parsePBKDF2 reports whether secret is a fully usable PBKDF2 secret. Both
// SecretConfigured and Verify go through it so the two cannot disagree about
// what is usable — a secret that passes the startup check and then fails every
// login is the worst of the available outcomes.
func parsePBKDF2(secret string) (pbkdf2Secret, bool) {
	m := pbkdf2Re.FindStringSubmatch(secret)
	if m == nil {
		return pbkdf2Secret{}, false
	}
	iterations, err := strconv.Atoi(m[1])
	if err != nil || iterations < 1 {
		return pbkdf2Secret{}, false
	}
	// Odd-length hex reaches here — the regex only constrains the alphabet.
	salt, err := hex.DecodeString(m[2])
	if err != nil || len(salt) == 0 {
		return pbkdf2Secret{}, false
	}
	want, err := hex.DecodeString(m[3])
	if err != nil || len(want) == 0 {
		return pbkdf2Secret{}, false
	}
	return pbkdf2Secret{iterations: iterations, salt: salt, want: want}, true
}

// Argon2id parameters. 64 MiB and 3 passes is the draft-RFC "second recommended
// option" — comfortably under a second on the kind of machine that runs a local
// LLM, and expensive to parallelise.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrMalformedSecret means the stored secret matches neither format. Callers
// must treat this as "no password configured is impossible, refuse to start"
// rather than "let everyone in".
var ErrMalformedSecret = errors.New("access_secret is not an Argon2id hash, a pbkdf2-sha256 secret, or a legacy salt:hash pair")

// NewSecret hashes a password for storage.
func NewSecret(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify checks password against a stored secret.
//
// needsRehash is true when the secret verified but is in the legacy format, and
// the caller should persist NewSecret(password) to complete the migration.
func Verify(secret, password string) (ok bool, needsRehash bool, err error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return false, false, nil
	}

	if strings.HasPrefix(secret, "$argon2id$") {
		ok, err := verifyArgon2(secret, password)
		return ok, false, err
	}

	if strings.HasPrefix(secret, pbkdf2Prefix) {
		p, ok := parsePBKDF2(secret)
		if !ok {
			return false, false, ErrMalformedSecret
		}
		// Derive to the stored hash's length, not a constant, so a launcher that
		// later widens the output still verifies.
		got, err := pbkdf2.Key(sha256.New, password, p.salt, p.iterations, len(p.want))
		if err != nil {
			return false, false, err
		}
		// needsRehash is deliberately false. The legacy form is rehashed because
		// one round of SHA-256 is not a KDF at all; PBKDF2-SHA256 at the
		// launcher's iteration count is, so there is no urgency. And the rehash
		// would not stick: the only thing that writes this format is launch.bat,
		// which hands us the secret through GEMMA_ACCESS_SECRET, and the
		// environment overrides config.toml (see config.applyEnv). We would
		// rewrite config.toml on every single login and read the PBKDF2 secret
		// back from the environment on every start.
		return subtle.ConstantTimeCompare(got, p.want) == 1, false, nil
	}

	if m := legacyRe.FindStringSubmatch(secret); m != nil {
		salt, want := strings.ToLower(m[1]), strings.ToLower(m[2])
		sum := sha256.Sum256([]byte(salt + password))
		got := hex.EncodeToString(sum[:])
		// Constant-time even though both sides are hex of a public-length
		// digest: a timing difference here leaks how many leading bytes matched.
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return true, true, nil
		}
		return false, false, nil
	}

	return false, false, ErrMalformedSecret
}

func verifyArgon2(secret, password string) (bool, error) {
	// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
	parts := strings.Split(secret, "$")
	if len(parts) != 6 {
		return false, ErrMalformedSecret
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrMalformedSecret
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}

	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrMalformedSecret
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrMalformedSecret
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrMalformedSecret
	}

	// Derive with the parameters recorded in the hash, not the current
	// constants, so tightening them later doesn't lock existing users out.
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// SecretConfigured reports whether a usable password is stored.
func SecretConfigured(secret string) bool {
	secret = strings.TrimSpace(secret)
	if strings.HasPrefix(secret, pbkdf2Prefix) {
		_, ok := parsePBKDF2(secret)
		return ok
	}
	return strings.HasPrefix(secret, "$argon2id$") || legacyRe.MatchString(secret)
}
