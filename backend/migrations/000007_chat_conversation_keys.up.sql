BEGIN;
ALTER TABLE conversations ADD COLUMN create_key_hash bytea
 CHECK (create_key_hash IS NULL OR (member_id IS NOT NULL AND octet_length(create_key_hash) = 32));
CREATE UNIQUE INDEX conversations_member_create_key_idx
 ON conversations (account_id, agent_id, member_id, create_key_hash)
 WHERE create_key_hash IS NOT NULL;
CREATE FUNCTION lock_chat_write(account uuid, member uuid, agent uuid, conversation uuid)
 RETURNS boolean LANGUAGE SQL SECURITY DEFINER
 BEGIN ATOMIC
  SELECT CASE WHEN EXISTS (
   SELECT 1 FROM account_members WHERE account_id=account AND user_id=member FOR SHARE
  ) THEN conversation IS NULL OR EXISTS (
   SELECT 1 FROM conversations
   WHERE account_id=account AND member_id=member AND agent_id=agent AND id=conversation FOR NO KEY UPDATE
  ) ELSE false END;
 END;
ALTER FUNCTION lock_chat_write(uuid,uuid,uuid,uuid) SET search_path TO pg_catalog,pg_temp;
REVOKE ALL ON FUNCTION lock_chat_write(uuid,uuid,uuid,uuid) FROM PUBLIC;
COMMIT;
