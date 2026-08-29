-- Search Automation SQLite Schema
-- Auto-created by Go on first run

CREATE TABLE IF NOT EXISTS proxies (
    id INTEGER PRIMARY KEY,
    ip TEXT NOT NULL,
    port INTEGER NOT NULL,
    protocol TEXT DEFAULT 'http',
    country TEXT,
    timezone TEXT,
    username TEXT DEFAULT '',
    password TEXT DEFAULT '',
    active INTEGER DEFAULT 1,
    latency_ms INTEGER,
    used_count INTEGER DEFAULT 0,
    last_used_at TIMESTAMP,
    blacklisted INTEGER DEFAULT 0,
    blacklist_reason TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS articles (
    id INTEGER PRIMARY KEY,
    domain TEXT NOT NULL,
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    meta_desc TEXT,
    topic TEXT,
    searched_count INTEGER DEFAULT 0,
    last_searched_at TIMESTAMP,
    first_searched_at TIMESTAMP,
    serp_position INTEGER,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY,
    article_id INTEGER NOT NULL,
    proxy_id INTEGER NOT NULL,
    engine TEXT NOT NULL,
    status TEXT DEFAULT 'pending',
    result_json TEXT,
    error TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    FOREIGN KEY (article_id) REFERENCES articles(id),
    FOREIGN KEY (proxy_id) REFERENCES proxies(id)
);

CREATE TABLE IF NOT EXISTS daily_stats (
    date TEXT PRIMARY KEY,
    total_search INTEGER DEFAULT 0,
    success INTEGER DEFAULT 0,
    fail INTEGER DEFAULT 0,
    captcha INTEGER DEFAULT 0,
    avg_dwell_seconds REAL,
    avg_serp_position REAL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_proxies_ip_port_user ON proxies(ip, port, username);
CREATE INDEX IF NOT EXISTS idx_proxies_active ON proxies(active, blacklisted);
CREATE INDEX IF NOT EXISTS idx_articles_searched ON articles(searched_count, last_searched_at);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status, created_at);
