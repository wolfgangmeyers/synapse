CREATE TABLE prompt_templates (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (length(name) > 0),
  CHECK (length(CAST(name AS BLOB)) <= 128),
  CHECK (instr(CAST(name AS BLOB), X'00') = 0),
  CHECK (length(body) > 0),
  CHECK (length(CAST(body AS BLOB)) <= 65536),
  CHECK (instr(CAST(body AS BLOB), X'00') = 0)
);
