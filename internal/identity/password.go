package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonVersion = 19
	argonMemory  = 65_536
	argonTime    = 3
	argonThreads = 1
	saltLength   = 16
	hashLength   = 32
)

var (
	ErrPasswordPolicy = errors.New("password does not meet policy")
	ErrHashOverloaded = errors.New("password hashing admission queue is full")
	ErrHashTimeout    = errors.New("password hashing admission timed out")

	//go:embed testdata/password-blocklist-v1.txt testdata/password-blocklist-v1-provenance.txt
	blocklistFile     embed.FS
	blockedPasswords  = loadBlocklist()
	passwordAdmission = newHashAdmission(2, 16)
)

type hashAdmission struct {
	mu         sync.Mutex
	permits    chan struct{}
	queued     int
	queueLimit int
	wait       time.Duration
}

func newHashAdmission(active, queued int) *hashAdmission {
	return &hashAdmission{permits: make(chan struct{}, active), queueLimit: queued, wait: 5 * time.Second}
}

func (g *hashAdmission) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if len(g.permits) < cap(g.permits) {
		g.permits <- struct{}{}
		g.mu.Unlock()
		return func() { <-g.permits }, nil
	}
	if g.queued >= g.queueLimit {
		g.mu.Unlock()
		return nil, ErrHashOverloaded
	}
	g.queued++
	g.mu.Unlock()

	deadline := time.Now().Add(g.wait)
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.permits <- struct{}{}:
		g.mu.Lock()
		g.queued--
		g.mu.Unlock()
		if err := ctx.Err(); err != nil {
			<-g.permits
			return nil, err
		}
		if !time.Now().Before(deadline) {
			<-g.permits
			return nil, ErrHashTimeout
		}
		return func() { <-g.permits }, nil
	case <-ctx.Done():
		g.mu.Lock()
		g.queued--
		g.mu.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
		g.mu.Lock()
		g.queued--
		g.mu.Unlock()
		return nil, ErrHashTimeout
	}
}

func validatePassword(password []byte, login, displayName string) error {
	if len(password) > 512 || !utf8.Valid(password) {
		return ErrPasswordPolicy
	}
	for _, b := range password {
		if b == 0 {
			return ErrPasswordPolicy
		}
	}
	count := utf8.RuneCount(password)
	if count < 15 || count > 128 {
		return ErrPasswordPolicy
	}
	folded := strings.ToLower(string(password))
	if login != "" && strings.Contains(folded, strings.ToLower(login)) || displayName != "" && strings.Contains(folded, strings.ToLower(displayName)) {
		return ErrPasswordPolicy
	}
	if _, ok := blockedPasswords[folded]; ok {
		return ErrPasswordPolicy
	}
	return nil
}

func loadBlocklist() map[string]struct{} {
	data, err := blocklistFile.ReadFile("testdata/password-blocklist-v1.txt")
	if err != nil {
		panic(err)
	}
	entries := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			entries[strings.ToLower(line)] = struct{}{}
		}
	}
	return entries
}

func hashPasswordWithPolicy(ctx context.Context, password []byte, login, displayName string) ([]byte, error) {
	if err := validatePassword(password, login, displayName); err != nil {
		return nil, err
	}
	return hashPassword(ctx, password)
}

func hashPassword(ctx context.Context, password []byte) ([]byte, error) {
	release, err := passwordAdmission.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	salt := make([]byte, saltLength)
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	hash := argon2.IDKey(password, salt, argonTime, argonMemory, argonThreads, hashLength)
	return []byte(fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argonVersion, argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))), nil
}

type parsedHash struct {
	salt, hash []byte
	memory     uint32
	time       uint32
	threads    uint8
}

func parsePasswordHash(encoded []byte) (parsedHash, error) {
	if len(encoded) > 256 {
		return parsedHash{}, errors.New("password hash exceeds safe bound")
	}
	parts := strings.Split(string(encoded), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return parsedHash{}, errors.New("invalid password hash")
	}
	var p parsedHash
	if !strings.HasPrefix(parts[2], "v=") {
		return p, errors.New("invalid password hash version")
	}
	version, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil || version != argonVersion {
		return p, errors.New("unsupported password hash version")
	}
	costs := strings.Split(parts[3], ",")
	if len(costs) != 3 {
		return p, errors.New("invalid password hash costs")
	}
	values := make(map[string]uint64, 3)
	for _, cost := range costs {
		kv := strings.SplitN(cost, "=", 2)
		if len(kv) != 2 {
			return p, errors.New("invalid password hash cost")
		}
		v, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return p, errors.New("invalid password hash cost")
		}
		if _, exists := values[kv[0]]; exists {
			return p, errors.New("duplicate password hash cost")
		}
		values[kv[0]] = v
	}
	m, mok := values["m"]
	tc, tok := values["t"]
	th, hok := values["p"]
	if !mok || !tok || !hok || m < 8192 || m > 262144 || tc < 1 || tc > 10 || th < 1 || th > 8 {
		return p, errors.New("password hash cost outside safe bounds")
	}
	p.memory, p.time, p.threads = uint32(m), uint32(tc), uint8(th)
	p.salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(p.salt) != saltLength {
		return parsedHash{}, errors.New("invalid password hash salt")
	}
	p.hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(p.hash) != hashLength {
		return parsedHash{}, errors.New("invalid password hash digest")
	}
	return p, nil
}

func verifyPasswordHash(ctx context.Context, encoded, candidate []byte) (bool, error) {
	if len(candidate) > 512 {
		return false, ErrPasswordPolicy
	}
	parsed, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	release, err := passwordAdmission.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	actual := argon2.IDKey(candidate, parsed.salt, parsed.time, parsed.memory, parsed.threads, uint32(len(parsed.hash)))
	return subtle.ConstantTimeCompare(actual, parsed.hash) == 1, nil
}

func hashNeedsUpgrade(encoded []byte) bool {
	p, err := parsePasswordHash(encoded)
	return err == nil && (p.memory < argonMemory || p.time < argonTime || p.threads < argonThreads)
}
