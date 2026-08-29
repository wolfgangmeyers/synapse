DROP TABLE IF EXISTS sentinel_relay_capabilities;
DROP TABLE IF EXISTS sentinel_occurrences;

UPDATE messages
SET sentinel_json = json_object('model', json_extract(sentinel_json, '$.model'))
WHERE sentinel_json IS NOT NULL;

UPDATE reminders
SET sentinel_json = json_object('model', json_extract(sentinel_json, '$.model'))
WHERE sentinel_json IS NOT NULL;
