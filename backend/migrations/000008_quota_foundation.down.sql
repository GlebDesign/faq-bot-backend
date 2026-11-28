BEGIN;
DROP FUNCTION grant_trial_quota(uuid,uuid,integer,integer);
DROP TABLE quota_usage_events;
DROP TABLE quota_reservations;
DROP TABLE quota_allocations;
DROP TABLE quota_packages;
ALTER TABLE ai_requests DROP CONSTRAINT ai_requests_account_id_key;
COMMIT;
