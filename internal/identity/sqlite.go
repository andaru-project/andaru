package identity

import _ "modernc.org/sqlite"

const applicationID = 0x414e4449

const schema = `
CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 login TEXT NOT NULL UNIQUE CHECK(length(login) BETWEEN 3 AND 64 AND login NOT GLOB '*[^a-z0-9._-]*' AND substr(login,1,1) GLOB '[a-z0-9]'),
 display_name TEXT NOT NULL CHECK(length(display_name) BETWEEN 0 AND 128),
 kind TEXT NOT NULL CHECK(kind IN ('human','service')),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE tombstones (account_id TEXT PRIMARY KEY, deleted_at INTEGER NOT NULL) STRICT;
CREATE TABLE account_roles (
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 role_id TEXT NOT NULL CHECK(
  role_id IN ('administrator','read-only') OR
  (length(role_id)=40 AND substr(role_id,1,4)='rol_' AND
   substr(role_id,5,8) NOT GLOB '*[^0-9a-f]*' AND substr(role_id,13,1)='-' AND
   substr(role_id,14,4) NOT GLOB '*[^0-9a-f]*' AND substr(role_id,18,1)='-' AND
   substr(role_id,19,1)='4' AND substr(role_id,20,3) NOT GLOB '*[^0-9a-f]*' AND
   substr(role_id,23,1)='-' AND substr(role_id,24,1) IN ('8','9','a','b') AND
   substr(role_id,25,3) NOT GLOB '*[^0-9a-f]*' AND substr(role_id,28,1)='-' AND
   substr(role_id,29,12) NOT GLOB '*[^0-9a-f]*')
 ),
 PRIMARY KEY(account_id,role_id)
) STRICT;
CREATE TABLE password_credentials (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 salt BLOB NOT NULL CHECK(length(salt)=16),
 hash BLOB NOT NULL CHECK(length(hash)=32),
 algorithm_version INTEGER NOT NULL CHECK(algorithm_version=19),
 memory_kib INTEGER NOT NULL CHECK(memory_kib BETWEEN 8192 AND 262144),
 time_cost INTEGER NOT NULL CHECK(time_cost BETWEEN 1 AND 10),
 parallelism INTEGER NOT NULL CHECK(parallelism BETWEEN 1 AND 8),
 updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE api_tokens (
 selector BLOB PRIMARY KEY CHECK(length(selector)=16),
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 secret_hash BLOB NOT NULL CHECK(length(secret_hash)=32),
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
 scopes TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER,
 revoked_at INTEGER,
 last_used_at INTEGER,
 revoke_on_password_change INTEGER NOT NULL CHECK(revoke_on_password_change IN (0,1))
) STRICT;
CREATE INDEX api_tokens_account_active ON api_tokens(account_id,expires_at) WHERE revoked_at IS NULL;
CREATE TABLE sessions (
 session_hash BLOB PRIMARY KEY CHECK(length(session_hash)=32),
 csrf_hash BLOB NOT NULL CHECK(length(csrf_hash)=32),
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 auth_epoch INTEGER NOT NULL CHECK(auth_epoch>=0),
 created_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL
) STRICT;
CREATE INDEX sessions_account ON sessions(account_id);
CREATE TABLE login_limits (
 bucket_hash BLOB PRIMARY KEY CHECK(length(bucket_hash)=32),
 failures INTEGER NOT NULL CHECK(failures BETWEEN 0 AND 5),
 window_started_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL
) STRICT;
CREATE TABLE metadata (key TEXT PRIMARY KEY, value INTEGER NOT NULL) STRICT;
CREATE TABLE revisions (
 revision INTEGER PRIMARY KEY CHECK(revision>=0),
 created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE operations (
 operation_id TEXT PRIMARY KEY,
 idempotency_key TEXT NOT NULL UNIQUE,
 request_hash BLOB NOT NULL CHECK(length(request_hash)=32),
 revision INTEGER NOT NULL REFERENCES revisions(revision),
 created_at INTEGER NOT NULL
) STRICT;
INSERT INTO metadata(key,value) VALUES('revision',0);
INSERT INTO revisions(revision,created_at) VALUES(0,unixepoch());
`
