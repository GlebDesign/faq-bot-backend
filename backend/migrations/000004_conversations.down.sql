BEGIN;
DROP TABLE ai_requests;
DROP TABLE message_sources;
DROP TABLE messages;
DROP TABLE conversations;
DROP FUNCTION check_conversation_member();
COMMIT;
