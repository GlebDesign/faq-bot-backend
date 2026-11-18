BEGIN;
CREATE TABLE agent_operations (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES accounts(id),
 actor_id uuid NOT NULL REFERENCES users(id),
 operation text NOT NULL CHECK (operation IN ('create','update','delete')),
 resource text NOT NULL,
 UNIQUE (id,account_id,operation,resource),
 key_hash bytea NOT NULL CHECK (octet_length(key_hash)=32),
 payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 CONSTRAINT agent_operations_scope_key UNIQUE (account_id,actor_id,operation,resource,key_hash),
 CHECK ((operation='create' AND resource='') OR (operation IN ('update','delete') AND resource ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')),
 CHECK (result IS NULL OR jsonb_typeof(result)='object')
);
CREATE TABLE agent_cleanup_requests (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 operation text GENERATED ALWAYS AS ('delete'::text) STORED,
 resource text GENERATED ALWAYS AS (agent_id::text) STORED,
 FOREIGN KEY (id,account_id,operation,resource) REFERENCES agent_operations(id,account_id,operation,resource),
 status text NOT NULL DEFAULT 'pending' CHECK (status='pending'),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (account_id,agent_id) REFERENCES agents(account_id,id),
 UNIQUE (account_id,agent_id)
);
COMMIT;
