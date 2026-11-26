BEGIN;
DROP FUNCTION lock_chat_write(uuid,uuid,uuid,uuid);
DROP INDEX conversations_member_create_key_idx;
ALTER TABLE conversations DROP COLUMN create_key_hash;
COMMIT;
