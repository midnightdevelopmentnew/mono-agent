-- 019_actions_platform_uppercase.sql
-- The GUI stores and filters actions.target_platform in uppercase (INSTAGRAM),
-- but the CLI stored it lowercase, so CLI-created actions were invisible in the
-- GUI's platform-filtered list. Execution paths re-normalize case themselves, so
-- normalizing storage to uppercase is safe. Also backfill created_at_ts (which the
-- GUI sorts by) from the legacy created_at epoch for rows the CLI left NULL.

UPDATE actions SET target_platform = UPPER(target_platform)
WHERE target_platform IS NOT NULL AND target_platform <> UPPER(target_platform);

UPDATE actions
SET created_at_ts = strftime('%Y-%m-%dT%H:%M:%SZ', created_at, 'unixepoch')
WHERE (created_at_ts IS NULL OR created_at_ts = '') AND created_at IS NOT NULL AND created_at > 0;
