-- Optional knowledge subsystem migration, applied by cmd/knowledge-migrate.
-- Existing six business migrations remain unchanged. Apply them first.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
CREATE SCHEMA IF NOT EXISTS knowledge;
CREATE SCHEMA IF NOT EXISTS ai;
CREATE TABLE IF NOT EXISTS knowledge.documents (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 name text NOT NULL,
 format text NOT NULL CHECK (format IN ('txt','md','text','pdf','docx')),
 storage_key text NOT NULL,
 source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[a-f0-9]{64}$'),
 current_version bigint NOT NULL CHECK (current_version>0),
 active_version bigint,
 active_generation uuid,
 status text NOT NULL CHECK (status IN ('pending','processing','ready','failed','deleting','deleted')),
 current_job_id uuid NOT NULL,
 knowledge_revision bigint NOT NULL,
 error_code text,
 deleted_at timestamptz,
 UNIQUE (account_id,agent_id,id),
 FOREIGN KEY (account_id,agent_id) REFERENCES public.agents(account_id,id),
 CHECK ((active_generation IS NULL) = (active_version IS NULL)),
 CHECK (active_version IS NULL OR active_version<=current_version)
);
CREATE TABLE IF NOT EXISTS knowledge.jobs (
 id uuid PRIMARY KEY, operation_id uuid NOT NULL,
 account_id uuid NOT NULL, agent_id uuid NOT NULL, document_id uuid NOT NULL,
 submission_hash bytea NOT NULL,
 payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','queued','processing','completed','failed')),
 result jsonb,
 error_code text,
 UNIQUE (account_id,agent_id,operation_id),
 FOREIGN KEY (account_id,agent_id,document_id) REFERENCES knowledge.documents(account_id,agent_id,id)
);
CREATE TABLE IF NOT EXISTS knowledge.outbox (
 job_id uuid PRIMARY KEY REFERENCES knowledge.jobs(id),
 payload jsonb NOT NULL,
 published_at timestamptz
);
CREATE TABLE IF NOT EXISTS ai.generations (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL, agent_id uuid NOT NULL, document_id uuid NOT NULL,
 document_version bigint NOT NULL, model_fingerprint text NOT NULL,
 dimension integer NOT NULL CHECK (dimension BETWEEN 1 AND 8192),
 chunk_count integer NOT NULL CHECK (chunk_count BETWEEN 1 AND 1000),
 complete boolean NOT NULL DEFAULT false,
 UNIQUE (account_id,agent_id,document_id,document_version),
 UNIQUE (account_id,agent_id,document_id,id)
);
CREATE TABLE IF NOT EXISTS ai.chunks (
 generation_id uuid NOT NULL REFERENCES ai.generations(id) ON DELETE CASCADE,
 chunk_number integer NOT NULL,
 chunk_id text NOT NULL,
 content text NOT NULL,
 page integer NOT NULL, heading text NOT NULL, start_char bigint NOT NULL,
 embedding public.vector NOT NULL,
 PRIMARY KEY (generation_id,chunk_number),
 UNIQUE (generation_id,chunk_id)
);
CREATE TABLE IF NOT EXISTS ai.receipts (
 job_id uuid PRIMARY KEY,
 identity_hash text NOT NULL,
 owner uuid,
 fence bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 result jsonb,
 published_at timestamptz
);
CREATE OR REPLACE VIEW knowledge.ai_document_metadata AS
 SELECT d.*, a.deleted_at AS agent_deleted_at, j.payload AS job_payload,
        j.status AS job_status
 FROM knowledge.documents d
 JOIN public.agents a ON a.account_id=d.account_id AND a.id=d.agent_id
 JOIN knowledge.jobs j ON j.id=d.current_job_id;
DO $roles$ BEGIN
 IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='faq_ai_runtime') THEN
  CREATE ROLE faq_ai_runtime NOLOGIN;
 END IF;
END $roles$;
REVOKE ALL ON SCHEMA ai FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA ai FROM PUBLIC;
GRANT USAGE ON SCHEMA ai,knowledge,public TO faq_ai_runtime;
GRANT SELECT ON knowledge.ai_document_metadata TO faq_ai_runtime;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA ai TO faq_ai_runtime;

DO $roles$ BEGIN
 IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='faq_go_knowledge_runtime') THEN
  CREATE ROLE faq_go_knowledge_runtime NOLOGIN;
 END IF;
END $roles$;
GRANT USAGE ON SCHEMA knowledge,ai,public TO faq_go_knowledge_runtime;
GRANT SELECT,UPDATE ON public.agents TO faq_go_knowledge_runtime;
GRANT SELECT,INSERT,UPDATE ON knowledge.documents,knowledge.jobs,knowledge.outbox TO faq_go_knowledge_runtime;
GRANT SELECT(id,account_id,agent_id,document_id,document_version,chunk_count,complete) ON ai.generations TO faq_go_knowledge_runtime;
GRANT SELECT(generation_id) ON ai.chunks TO faq_go_knowledge_runtime;
