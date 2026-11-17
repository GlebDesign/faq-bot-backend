BEGIN;
CREATE TABLE audit_events (
 id uuid PRIMARY KEY,
 event_key text NOT NULL UNIQUE CHECK (event_key ~ '^[0-9a-f]{64}$'),
 schema_version smallint NOT NULL DEFAULT 1 CHECK (schema_version = 1),
 scope text NOT NULL CHECK (scope IN ('tenant', 'platform', 'unattributed')),
 account_id uuid REFERENCES accounts(id),
 actor_type varchar(32) NOT NULL CHECK (actor_type IN ('user', 'visitor', 'partner', 'service', 'platform_admin', 'unknown')),
 actor_id text,
 action varchar(120) NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$'),
 entity_type varchar(80) NOT NULL CHECK (entity_type ~ '^[a-z][a-z0-9_]*$'),
 entity_id text CHECK (entity_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 outcome varchar(20) NOT NULL CHECK (outcome IN ('success', 'denied', 'failure', 'pending')),
 occurred_at timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 request_id text CHECK (octet_length(request_id) BETWEEN 1 AND 128 AND request_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]*$'),
 trace_id text CHECK (octet_length(trace_id) BETWEEN 1 AND 128 AND trace_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]*$'),
 source_service varchar(64) NOT NULL CHECK (source_service IN ('go-api', 'ai-worker', 'auth-gateway', 'infra')),
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 4096),
 CHECK ((scope = 'tenant') = (account_id IS NOT NULL)),
 CHECK (scope <> 'platform' OR (action LIKE 'platform.%' AND actor_type IN ('platform_admin', 'service'))),
 CHECK (action NOT LIKE 'platform.%' OR scope = 'platform'),
 CHECK (scope <> 'unattributed' OR action IN ('auth.login_failed', 'auth.access_denied', 'audit.access_denied', 'security.rate_limit_exceeded', 'security.suspicious_activity')),
 CHECK (
  (actor_type IN ('user', 'partner', 'platform_admin') AND actor_id IS NOT NULL AND actor_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') OR
  (actor_type = 'visitor' AND (actor_id IS NULL OR actor_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')) OR
  (actor_type = 'service' AND actor_id IS NOT NULL AND actor_id IN ('go-api', 'ai-worker', 'auth-gateway', 'infra')) OR
  (actor_type = 'unknown' AND actor_id IS NULL)
 ),
 CHECK (actor_type <> 'unknown' OR outcome IN ('denied', 'failure')),
 CHECK (actor_type NOT IN ('unknown', 'visitor', 'partner') OR entity_type IN ('auth', 'audit', 'security')),
 CHECK (entity_id IS NOT NULL OR entity_type IN ('auth', 'audit', 'security', 'platform'))
);
CREATE INDEX audit_account_time_idx ON audit_events (account_id, recorded_at DESC, id DESC);
CREATE INDEX audit_account_action_time_idx ON audit_events (account_id, action, recorded_at DESC, id DESC);
CREATE INDEX audit_entity_time_idx ON audit_events (account_id, entity_type, entity_id, recorded_at DESC, id DESC);
CREATE INDEX audit_request_time_idx ON audit_events (request_id, recorded_at DESC, id DESC) WHERE request_id IS NOT NULL;
COMMIT;
