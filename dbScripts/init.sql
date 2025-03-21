-- Create database if it doesn't exist
SELECT 'CREATE DATABASE "olympus"'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'olympus')\gexec

\connect olympus

-- Create matchHistory table
CREATE TABLE IF NOT EXISTS "matchHistory" (
    "matchID"               VARCHAR(50) NOT NULL,
    "gameVer"               VARCHAR(50) NOT NULL,
    "riotID"                VARCHAR(100) NOT NULL,
    "gameDuration"          INT NOT NULL,
    "gameCreationTimestamp" INT NOT NULL,
    "gameEndTimestamp"      INT NOT NULL,
    "teamOnePUUID"          TEXT[] NOT NULL,
    "teamTwoPUUID"          TEXT[] NOT NULL,
    "participants"          JSON NOT NULL,
    CONSTRAINT unique_pair_index UNIQUE ("matchID", "riotID")
);

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
