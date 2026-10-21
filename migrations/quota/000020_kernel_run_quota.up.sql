CREATE TABLE IF NOT EXISTS kernel_run_quota_day (
    project TEXT NOT NULL,
    day     DATE NOT NULL,
    runs    BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (project, day)
);
