BEGIN;
ALTER TABLE ai_requests ADD CONSTRAINT ai_requests_account_id_key UNIQUE (account_id, id);

CREATE TABLE quota_packages (
 id uuid PRIMARY KEY,
 name text NOT NULL CHECK (btrim(name) <> ''),
 request_count integer NOT NULL CHECK (request_count > 0)
);

CREATE TABLE quota_allocations (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL REFERENCES accounts(id),
 kind text NOT NULL CHECK (kind IN ('trial', 'purchased')),
 package_id uuid REFERENCES quota_packages(id),
 trial_account_id uuid GENERATED ALWAYS AS (CASE WHEN kind = 'trial' THEN account_id END) STORED
  UNIQUE REFERENCES trial_bootstrap_intents(account_id),
 request_count integer NOT NULL CHECK (request_count > 0),
 granted_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz,
 UNIQUE (account_id, id),
 CHECK (
  (kind = 'trial' AND package_id IS NULL AND expires_at IS NOT NULL AND expires_at > granted_at) OR
  (kind = 'purchased' AND package_id IS NOT NULL AND expires_at IS NULL)
 )
);

CREATE TABLE quota_reservations (
 ai_request_id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 allocation_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'released', 'consumed')),
 reserved_at timestamptz NOT NULL DEFAULT now(),
 deadline_at timestamptz NOT NULL CHECK (deadline_at > reserved_at),
 finalized_at timestamptz,
 UNIQUE (account_id, ai_request_id, state),
 FOREIGN KEY (account_id, ai_request_id) REFERENCES ai_requests(account_id, id),
 FOREIGN KEY (account_id, allocation_id) REFERENCES quota_allocations(account_id, id),
 CHECK ((state = 'active') = (finalized_at IS NULL)),
 CHECK (finalized_at >= reserved_at),
 CHECK (state <> 'consumed' OR finalized_at < deadline_at)
);
CREATE INDEX quota_reservations_allocation_idx ON quota_reservations (account_id, allocation_id);
CREATE INDEX quota_reservations_active_deadline_idx ON quota_reservations (deadline_at, ai_request_id)
 WHERE state = 'active';

CREATE TABLE quota_usage_events (
 operation_id uuid PRIMARY KEY,
 ai_request_id uuid NOT NULL UNIQUE,
 account_id uuid NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 reservation_state text GENERATED ALWAYS AS ('consumed'::text) STORED,
 FOREIGN KEY (account_id, ai_request_id, reservation_state)
  REFERENCES quota_reservations(account_id, ai_request_id, state)
);

CREATE FUNCTION grant_trial_quota(target_account uuid, allocation uuid, quantity integer, duration_days integer)
 RETURNS void LANGUAGE SQL SECURITY DEFINER
 BEGIN ATOMIC
  INSERT INTO quota_allocations (id, account_id, kind, request_count, granted_at, expires_at)
   SELECT allocation, i.account_id, 'trial', quantity, i.created_at,
          i.created_at + duration_days * interval '24 hours'
   FROM trial_bootstrap_intents i WHERE i.account_id = target_account
   ON CONFLICT (trial_account_id) DO NOTHING;
 END;
ALTER FUNCTION grant_trial_quota(uuid,uuid,integer,integer) SET search_path TO pg_catalog,pg_temp;
REVOKE ALL ON FUNCTION grant_trial_quota(uuid,uuid,integer,integer) FROM PUBLIC;
COMMIT;
