BEGIN;
CREATE TABLE conversations (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 visitor_id text CHECK (visitor_id IS NULL OR btrim(visitor_id) <> ''),
 member_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (account_id, agent_id) REFERENCES agents(account_id, id),
 CHECK ((visitor_id IS NULL) <> (member_id IS NULL)),
 UNIQUE (account_id, agent_id, id),
 UNIQUE (account_id, agent_id, id, visitor_id),
 UNIQUE (account_id, agent_id, id, member_id)
);
CREATE FUNCTION check_conversation_member() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
 present boolean;
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF NEW.member_id IS NOT DISTINCT FROM OLD.member_id AND NEW.account_id = OLD.account_id THEN
   RETURN NEW;
  END IF;
 END IF;
 IF NEW.member_id IS NOT NULL THEN
  EXECUTE pg_catalog.format('SELECT true FROM %I.account_members WHERE user_id=$1 AND account_id=$2 FOR KEY SHARE', TG_TABLE_SCHEMA)
   INTO present USING NEW.member_id, NEW.account_id;
  IF present IS DISTINCT FROM true THEN
   RAISE EXCEPTION USING ERRCODE = '23503', MESSAGE = 'conversation membership is absent';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION check_conversation_member() FROM PUBLIC;
CREATE TRIGGER conversations_member_check BEFORE INSERT OR UPDATE OF member_id, account_id ON conversations
FOR EACH ROW EXECUTE FUNCTION check_conversation_member();
CREATE INDEX conversations_visitor_page_idx ON conversations (account_id, agent_id, visitor_id, created_at DESC, id DESC) WHERE visitor_id IS NOT NULL;
CREATE INDEX conversations_member_page_idx ON conversations (account_id, agent_id, member_id, created_at DESC, id DESC) WHERE member_id IS NOT NULL;
CREATE TABLE messages (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 question text NOT NULL CHECK (btrim(question, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000') <> ''),
 answer text,
 status text NOT NULL DEFAULT 'accepted' CHECK (status IN ('accepted', 'generating', 'completed', 'no_answer', 'failed', 'cancelled')),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (account_id, agent_id, conversation_id) REFERENCES conversations(account_id, agent_id, id),
 UNIQUE (account_id, agent_id, conversation_id, id),
 CHECK ((status IN ('completed', 'no_answer')) = (answer IS NOT NULL)),
 CHECK (answer IS NULL OR btrim(answer) <> '')
);
CREATE INDEX messages_conversation_page_idx ON messages (account_id, agent_id, conversation_id, created_at DESC, id DESC);
CREATE TABLE message_sources (
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 citation_id text NOT NULL CHECK (btrim(citation_id) <> ''),
 display_name text NOT NULL CHECK (btrim(display_name) <> ''),
 locator text NOT NULL,
 excerpt text NOT NULL,
 document_id uuid NOT NULL,
 document_version bigint NOT NULL CHECK (document_version >= 1),
 knowledge_revision bigint NOT NULL CHECK (knowledge_revision >= 1),
 PRIMARY KEY (message_id, citation_id),
 FOREIGN KEY (account_id, agent_id, conversation_id, message_id) REFERENCES messages(account_id, agent_id, conversation_id, id)
);
CREATE TABLE ai_requests (
 id uuid PRIMARY KEY,
 account_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 visitor_id text,
 member_id uuid,
 operation text NOT NULL CHECK (operation IN ('widget_message', 'test_message')),
 idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9_-]{1,128}$'),
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
 state text NOT NULL DEFAULT 'accepted' CHECK (state IN ('accepted', 'generating', 'completed', 'no_answer', 'failed', 'cancelled')),
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (account_id, agent_id, conversation_id, visitor_id) REFERENCES conversations(account_id, agent_id, id, visitor_id),
 FOREIGN KEY (account_id, agent_id, conversation_id, member_id) REFERENCES conversations(account_id, agent_id, id, member_id),
 FOREIGN KEY (account_id, agent_id, conversation_id, message_id) REFERENCES messages(account_id, agent_id, conversation_id, id),
 CHECK ((visitor_id IS NULL) <> (member_id IS NULL)),
 CHECK ((operation = 'widget_message') = (visitor_id IS NOT NULL)),
 CHECK ((state IN ('completed', 'no_answer', 'failed', 'cancelled')) = (result IS NOT NULL)),
 CHECK (result IS NULL OR (jsonb_typeof(result) = 'object' AND (result->>'status') IS NOT DISTINCT FROM state)),
 CONSTRAINT ai_requests_scope_key UNIQUE NULLS NOT DISTINCT (account_id, agent_id, visitor_id, member_id, operation, idempotency_key)
);
CREATE INDEX ai_requests_message_idx ON ai_requests (account_id, agent_id, conversation_id, message_id);
COMMIT;
