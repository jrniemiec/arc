package sqlite

const schema = `
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS collections (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  TEXT
);

CREATE TABLE IF NOT EXISTS articles (
    id              TEXT PRIMARY KEY,
    title           TEXT,
    url             TEXT,
    source_type     TEXT,
    feed            TEXT,
    author          TEXT,
    published_at    TEXT,
    language        TEXT,
    ingested_at     TEXT NOT NULL,
    read_at         TEXT,
    played_at       TEXT,
    summary_model   TEXT,
    summary_style   TEXT,
    flash_model     TEXT,
    flashcard_model TEXT,
    flashcard_style TEXT,
    embed_model     TEXT,
    quality_score   REAL,
    root_path       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS article_collections (
    article_id    TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    PRIMARY KEY (article_id, collection_id)
);

CREATE TABLE IF NOT EXISTS tags (
    article_id  TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'llm',
    PRIMARY KEY (article_id, tag)
);

CREATE TABLE IF NOT EXISTS relations (
    from_id      TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    to_id        TEXT NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    type         TEXT NOT NULL,
    detected_by  TEXT NOT NULL,
    detected_at  TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id, type)
);

CREATE VIRTUAL TABLE IF NOT EXISTS articles_fts USING fts5(
    article_id,
    title,
    summary,
    tokenize = 'porter unicode61'
);

CREATE INDEX IF NOT EXISTS idx_articles_ingested_at  ON articles(ingested_at);
CREATE INDEX IF NOT EXISTS idx_articles_source_type  ON articles(source_type);
CREATE INDEX IF NOT EXISTS idx_tags_tag              ON tags(tag);
CREATE INDEX IF NOT EXISTS idx_relations_from        ON relations(from_id);
CREATE INDEX IF NOT EXISTS idx_relations_to          ON relations(to_id);
`

// migrations are ALTER TABLE statements applied after the schema.
// Each is attempted once; "duplicate column name" and "already exists" errors
// are silently ignored so the list is safe to re-run on existing databases.
const migrations = `
ALTER TABLE articles ADD COLUMN agent_run_id   TEXT;
ALTER TABLE articles ADD COLUMN agent_verdict  TEXT;
ALTER TABLE articles ADD COLUMN agent_reason   TEXT;
CREATE INDEX IF NOT EXISTS idx_articles_agent_run ON articles(agent_run_id);
ALTER TABLE articles ADD COLUMN favorited_at   TEXT;
CREATE INDEX IF NOT EXISTS idx_articles_favorited ON articles(favorited_at);
ALTER TABLE articles ADD COLUMN num_id INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_articles_num_id ON articles(num_id);
ALTER TABLE collections ADD COLUMN num_id INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_collections_num_id ON collections(num_id);
CREATE VIRTUAL TABLE IF NOT EXISTS collections_fts USING fts5(
    collection_id,
    name,
    description,
    tokenize = 'porter unicode61'
);
`
