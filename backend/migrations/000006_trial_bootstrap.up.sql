BEGIN;
CREATE TABLE trial_bootstrap_intents (
 account_id uuid PRIMARY KEY REFERENCES accounts(id),
 state text NOT NULL DEFAULT 'pending' CHECK (state = 'pending'),
 created_at timestamptz NOT NULL DEFAULT now()
);
COMMIT;
