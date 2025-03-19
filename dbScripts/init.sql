-- Create matchHistory table
CREATE TABLE "matchHistory" (
    "matchID"               VARCHAR(50) NOT NULL,
    "gameVer"               VARCHAR(50) NOT NULL,
    "riotID"                VARCHAR(100) NOT NULL,
    "gameDuration"          VARCHAR(20) NOT NULL,
    "gameCreationTimestamp" VARCHAR(20) NOT NULL,
    "gameEndTimestamp"      VARCHAR(20) NOT NULL,
    "teamOnePUUID"          TEXT[] NOT NULL,
    "teamTwoPUUID"          TEXT[] NOT NULL,
    "participants"          JSON NOT NULL,
    CONSTRAINT unique_pair_index UNIQUE ("matchID", "riotID")
);

-- Create user and grant privileges
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sawa') THEN
        CREATE USER sawa WITH PASSWORD 'sawa';
    END IF;
END $$;
GRANT ALL PRIVILEGES ON DATABASE "matchHistory" TO sawa;