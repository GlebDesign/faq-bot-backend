BEGIN;
CREATE TABLE agents (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES accounts(id),
 public_id text NOT NULL UNIQUE CHECK (public_id ~ '^[A-Za-z0-9_-]{1,80}$'),
 name text NOT NULL CHECK (btrim(name, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000') <> '' AND char_length(name) <= 100),
 website text NOT NULL DEFAULT '',
 description text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'deleted')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
 knowledge_revision bigint NOT NULL DEFAULT 0 CHECK (knowledge_revision >= 0),
 active_knowledge_revision bigint NOT NULL DEFAULT 0 CHECK (active_knowledge_revision BETWEEN 0 AND knowledge_revision),
 config_revision bigint NOT NULL DEFAULT 1 CHECK (config_revision >= 1),
 published_config_revision bigint NOT NULL DEFAULT 0 CHECK (published_config_revision BETWEEN 0 AND config_revision),
 public_sources_enabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 UNIQUE (account_id, id),
 CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
 CHECK (deleted_at IS NULL OR deleted_at >= created_at),
 CHECK (status <> 'published' OR (active_knowledge_revision > 0 AND published_config_revision > 0))
);
CREATE INDEX agents_account_page_idx ON agents (account_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
COMMIT;
