package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net"
)

const (
	loginWindowSeconds  = int64(15 * 60)
	sourceExpirySeconds = int64(24 * 60 * 60)
	maximumBackoff      = int64(15 * 60)
	maximumBackoffLevel = 10
)

type sourceBucket struct {
	hash [32]byte
	kind string
}

func loadLimiterKey(ctx context.Context, db *sql.DB, allowCreate bool) ([32]byte, error) {
	var key [32]byte
	var stored []byte
	err := db.QueryRowContext(ctx, "SELECT secret FROM limiter_key WHERE id=1").Scan(&stored)
	if err == nil {
		if len(stored) != len(key) {
			return key, errors.New("invalid identity limiter key")
		}
		copy(key[:], stored)
		return key, nil
	}
	if err != sql.ErrNoRows {
		return key, err
	}
	if !allowCreate {
		return key, errors.New("missing identity limiter key")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return key, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT secret FROM limiter_key WHERE id=1").Scan(&stored); err == sql.ErrNoRows {
		if !allowCreate {
			return key, errors.New("missing identity limiter key")
		}
		if _, err = io.ReadFull(rand.Reader, key[:]); err != nil {
			return key, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO limiter_key(id,secret) VALUES(1,?)", key[:]); err != nil {
			return key, err
		}
	} else if err != nil {
		return key, err
	} else {
		if len(stored) != len(key) {
			return key, errors.New("invalid identity limiter key")
		}
		copy(key[:], stored)
	}
	if err = tx.Commit(); err != nil {
		return [32]byte{}, err
	}
	return key, nil
}

func (s *store) bucketHash(domain string, value []byte) [32]byte {
	mac := hmac.New(sha256.New, s.limiterKey[:])
	_, _ = io.WriteString(mac, domain)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(value)
	var hash [32]byte
	copy(hash[:], mac.Sum(nil))
	return hash
}

func limiterLoginKey(login string) string {
	if canonical, err := canonicalLogin(login); err == nil {
		return canonical
	}
	if len(login) > 64 {
		login = login[:64]
	}
	key := []byte(login)
	for i, b := range key {
		if b >= 'A' && b <= 'Z' {
			key[i] = b + ('a' - 'A')
		}
	}
	return string(key)
}

func (s *store) loginBucketHash(login string) [32]byte {
	return s.bucketHash("login", []byte(limiterLoginKey(login)))
}

func (s *store) sourceBuckets(source string) ([2]sourceBucket, int, error) {
	var buckets [2]sourceBucket
	if source == "" {
		return buckets, 0, nil
	}
	if len(source) > 64 {
		return buckets, 0, ErrInvalid
	}
	ip := net.ParseIP(source)
	if ip == nil {
		return buckets, 0, ErrInvalid
	}
	if v4 := ip.To4(); v4 != nil {
		buckets[0] = sourceBucket{hash: s.bucketHash("source_v4", v4), kind: "exact"}
		return buckets, 1, nil
	}
	v6 := ip.To16()
	if v6 == nil {
		return buckets, 0, ErrInvalid
	}
	buckets[0] = sourceBucket{hash: s.bucketHash("source_v6", v6), kind: "exact"}
	prefix := make(net.IP, net.IPv6len)
	copy(prefix, v6)
	for i := 8; i < len(prefix); i++ {
		prefix[i] = 0
	}
	buckets[1] = sourceBucket{hash: s.bucketHash("source_v6_64", prefix), kind: "ipv6_64"}
	return buckets, 2, nil
}

func (s *store) beginLogin(ctx context.Context, loginHash [32]byte, sources [2]sourceBucket, sourceCount int, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	var loginRows, sourceRows int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_limits").Scan(&loginRows); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_limits").Scan(&sourceRows); err != nil {
		return err
	}
	var loginSaturated, sourceSaturated int
	if err = tx.QueryRowContext(ctx, "SELECT login_saturated,source_saturated FROM limiter_overload WHERE id=1").Scan(&loginSaturated, &sourceSaturated); err != nil {
		return err
	}
	if loginRows < MaxLoginLimitRows && loginSaturated != 0 {
		loginSaturated = 0
	}
	if sourceRows < MaxSourceLimitRows && sourceSaturated != 0 {
		sourceSaturated = 0
	}
	if loginSaturated == 0 || sourceSaturated == 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET login_saturated=?,source_saturated=? WHERE id=1", loginSaturated, sourceSaturated); err != nil {
			return err
		}
	}
	if sourceSaturated != 0 && sourceRows >= MaxSourceLimitRows {
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrRateLimited
	}
	var loginExists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&loginExists); err != nil {
		return err
	}
	if loginRows >= MaxLoginLimitRows && loginExists == 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET login_saturated=1 WHERE id=1"); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrRateLimited
	}
	missingSources := 0
	for i := range sourceCount {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_limits WHERE bucket_hash=?", sources[i].hash[:]).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			missingSources++
		}
	}
	if sourceRows+missingSources > MaxSourceLimitRows {
		if _, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET source_saturated=1 WHERE id=1"); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrRateLimited
	}
	var backoffUntil int64
	err = tx.QueryRowContext(ctx, "SELECT backoff_until FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&backoffUntil)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if backoffUntil > now {
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrRateLimited
	}
	for i := range sourceCount {
		err = tx.QueryRowContext(ctx, "SELECT backoff_until FROM source_limits WHERE bucket_hash=?", sources[i].hash[:]).Scan(&backoffUntil)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if backoffUntil > now {
			if err = tx.Commit(); err != nil {
				return err
			}
			return ErrRateLimited
		}
	}
	return tx.Commit()
}

func nextFailure(failures, level int, windowStarted, now int64) (int, int, int64, int64) {
	if now-windowStarted >= loginWindowSeconds {
		failures, level = 0, 0
		windowStarted = now
	}
	if failures < 5 {
		failures++
	}
	backoffUntil := int64(0)
	if failures == 5 {
		if level < maximumBackoffLevel {
			level++
		}
		delay := int64(8) << (level - 1)
		if delay > maximumBackoff {
			delay = maximumBackoff
		}
		backoffUntil = now + delay
	}
	return failures, level, windowStarted, backoffUntil
}

func (s *store) recordLoginFailure(ctx context.Context, loginHash [32]byte, sources [2]sourceBucket, sourceCount int, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	if err = recordLoginBucketFailure(ctx, tx, loginHash, now); err != nil {
		return err
	}
	for i := range sourceCount {
		if err = recordSourceBucketFailure(ctx, tx, sources[i], now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordLoginBucketFailure(ctx context.Context, tx *sql.Tx, hash [32]byte, now int64) error {
	var failures, level int
	var windowStarted, backoffUntil int64
	err := tx.QueryRowContext(ctx, "SELECT failures,window_started_at,backoff_level,backoff_until FROM login_limits WHERE bucket_hash=?", hash[:]).Scan(&failures, &windowStarted, &level, &backoffUntil)
	if err == sql.ErrNoRows {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_limits").Scan(&count); err != nil {
			return err
		}
		if count >= MaxLoginLimitRows {
			_, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET login_saturated=1 WHERE id=1")
			return err
		}
		failures, level, windowStarted, backoffUntil = nextFailure(0, 0, now, now)
		_, err = tx.ExecContext(ctx, "INSERT INTO login_limits(bucket_hash,failures,window_started_at,expires_at,backoff_level,backoff_until) VALUES(?,?,?,?,?,?)", hash[:], failures, windowStarted, now+loginWindowSeconds, level, backoffUntil)
		return err
	}
	if err != nil {
		return err
	}
	failures, level, windowStarted, backoffUntil = nextFailure(failures, level, windowStarted, now)
	_, err = tx.ExecContext(ctx, "UPDATE login_limits SET failures=?,window_started_at=?,expires_at=?,backoff_level=?,backoff_until=? WHERE bucket_hash=?", failures, windowStarted, now+loginWindowSeconds, level, backoffUntil, hash[:])
	return err
}

func recordSourceBucketFailure(ctx context.Context, tx *sql.Tx, bucket sourceBucket, now int64) error {
	var failures, level int
	var windowStarted, backoffUntil int64
	err := tx.QueryRowContext(ctx, "SELECT failures,window_started_at,backoff_level,backoff_until FROM source_limits WHERE bucket_hash=?", bucket.hash[:]).Scan(&failures, &windowStarted, &level, &backoffUntil)
	if err == sql.ErrNoRows {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_limits").Scan(&count); err != nil {
			return err
		}
		if count >= MaxSourceLimitRows {
			_, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET source_saturated=1 WHERE id=1")
			return err
		}
		failures, level, windowStarted, backoffUntil = nextFailure(0, 0, now, now)
		_, err = tx.ExecContext(ctx, "INSERT INTO source_limits(bucket_hash,bucket_kind,failures,window_started_at,backoff_level,backoff_until,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?,?)", bucket.hash[:], bucket.kind, failures, windowStarted, level, backoffUntil, now, now+sourceExpirySeconds)
		return err
	}
	if err != nil {
		return err
	}
	failures, level, windowStarted, backoffUntil = nextFailure(failures, level, windowStarted, now)
	_, err = tx.ExecContext(ctx, "UPDATE source_limits SET failures=?,window_started_at=?,backoff_level=?,backoff_until=?,last_seen_at=?,expires_at=? WHERE bucket_hash=?", failures, windowStarted, level, backoffUntil, now, now+sourceExpirySeconds, bucket.hash[:])
	return err
}

func (s *store) recordLoginSuccess(ctx context.Context, loginHash [32]byte, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_limits WHERE bucket_hash=?", loginHash[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_limits WHERE expires_at<=?", now); err != nil {
		return err
	}
	var loginRows, sourceRows int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_limits").Scan(&loginRows); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_limits").Scan(&sourceRows); err != nil {
		return err
	}
	loginFree, sourceFree := 0, 0
	if loginRows < MaxLoginLimitRows {
		loginFree = 1
	}
	if sourceRows < MaxSourceLimitRows {
		sourceFree = 1
	}
	if loginFree == 1 || sourceFree == 1 {
		if _, err = tx.ExecContext(ctx, "UPDATE limiter_overload SET login_saturated=CASE WHEN ? THEN 0 ELSE login_saturated END,source_saturated=CASE WHEN ? THEN 0 ELSE source_saturated END WHERE id=1", loginFree, sourceFree); err != nil {
			return err
		}
	}
	return tx.Commit()
}
