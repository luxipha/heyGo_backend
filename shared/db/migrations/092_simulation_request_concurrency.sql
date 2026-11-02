-- Historical runs used a serial action queue. New runs default to eight clients.
ALTER TABLE simulation_runs ADD COLUMN request_concurrency INTEGER NOT NULL DEFAULT 1 CHECK(request_concurrency BETWEEN 1 AND 100);
ALTER TABLE simulation_runs ALTER COLUMN request_concurrency SET DEFAULT 8;
