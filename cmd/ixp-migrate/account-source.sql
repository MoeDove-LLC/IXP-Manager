-- Read-only modern-schema export from a consistent offline MySQL copy.
-- Older user schema: replace its created_at key/column with created.
-- No credential, MFA, token, preference, or login metadata is selected.
SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ;
START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY;
SELECT JSON_OBJECT('tables', JSON_OBJECT(
  'user', COALESCE((
    SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'id', CAST(u.id AS CHAR),
      'username', u.username,
      'name', u.name,
      'email', u.email,
      'disabled', u.disabled,
      'custid', CAST(u.custid AS CHAR),
      'created_at', CAST(u.created_at AS CHAR)
    )) FROM `user` AS u
  ), JSON_ARRAY()),
  'customer_to_users', COALESCE((
    SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'id', CAST(c.id AS CHAR),
      'user_id', CAST(c.user_id AS CHAR),
      'customer_id', CAST(c.customer_id AS CHAR),
      'privs', c.privs,
      'created_at', CAST(c.created_at AS CHAR)
    )) FROM customer_to_users AS c
  ), JSON_ARRAY())
));
COMMIT;
