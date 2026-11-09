-- Increase max connections. Keep this >= the sum of all pgx pool sizes
-- (matchmaking 50 + game server 50 + platform 50) plus headroom.
ALTER SYSTEM SET max_connections = 300;

-- Create database if it doesn't exist
SELECT 'CREATE DATABASE "olympus"'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'olympus')\gexec

\connect olympus

-- Create matchHistory table
CREATE TABLE IF NOT EXISTS "matchHistory" (
    "matchID"               VARCHAR(50) NOT NULL,
    "gameVer"               VARCHAR(50) NOT NULL,
    "puuid"                VARCHAR(100) NOT NULL,
    "gameDuration"          INT NOT NULL,
    "gameCreationTimestamp" INT NOT NULL,
    "gameEndTimestamp"      INT NOT NULL,
    "teamOnePUUID"          TEXT[] NOT NULL,
    "teamTwoPUUID"          TEXT[] NOT NULL,
    "participants"          JSON NOT NULL,
    CONSTRAINT unique_pair_index UNIQUE ("matchID", "puuid")
);

-- /matchHistory filters on puuid, but the unique constraint above is ordered by
-- matchID first, so it cannot serve that lookup. Without this index the query
-- degrades to a sequential scan as the table grows.
CREATE INDEX IF NOT EXISTS idx_match_history_puuid ON "matchHistory" ("puuid");

-- /matchHistory now returns only the most recent N matches (ORDER BY
-- "gameEndTimestamp" DESC, "matchID" DESC LIMIT N). The plain puuid index can
-- find the rows but Postgres still has to read the player's entire history and
-- sort it. This composite index matches the ORDER BY so it can stop after N
-- heap reads, which is what keeps the endpoint cheap after a long run.
CREATE INDEX IF NOT EXISTS idx_match_history_puuid_recent
    ON "matchHistory" ("puuid", "gameEndTimestamp" DESC, "matchID" DESC);

-- Create summonerRankedInfo table
CREATE TABLE IF NOT EXISTS "summonerRankedInfo" (
    "puuid"         VARCHAR(100) NOT NULL,
    "riotName"      VARCHAR(45) NOT NULL,
    "riotTag"       VARCHAR(45) NOT NULL,
    "rank"          INT NOT NULL,
    "wins"          INT NOT NULL,
    "losses"        INT NOT NULL,
    PRIMARY KEY ("puuid")
);

-- Create user and grant privileges
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sawa') THEN
        CREATE USER sawa WITH PASSWORD 'sawa';
    END IF;
END $$;

GRANT ALL PRIVILEGES ON DATABASE "olympus" TO sawa;
GRANT ALL ON SCHEMA public TO sawa;
