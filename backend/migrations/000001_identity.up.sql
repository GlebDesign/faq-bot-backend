BEGIN;
CREATE TABLE users (
 id uuid PRIMARY KEY,
 auth_subject text NOT NULL UNIQUE CHECK (auth_subject <> ''),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE accounts (
 id uuid PRIMARY KEY,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE account_members (
 user_id uuid NOT NULL REFERENCES users(id),
 account_id uuid NOT NULL REFERENCES accounts(id),
 role text NOT NULL CHECK (role IN ('owner', 'admin')),
 audit_read boolean NOT NULL DEFAULT false,
 PRIMARY KEY (user_id, account_id)
);
CREATE TABLE sessions (
 token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
 csrf text NOT NULL,
 user_id uuid REFERENCES users(id),
 account_id uuid REFERENCES accounts(id),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 idle_expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 FOREIGN KEY (user_id, account_id) REFERENCES account_members(user_id, account_id) ON DELETE CASCADE,
 CHECK ((user_id IS NULL) = (account_id IS NULL)),
 CHECK (expires_at > created_at)
);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);
COMMIT;
