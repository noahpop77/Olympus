package endpoints

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof" // Import pprof package
	"time"
)

// Modify PrintJsonHandler to accept the dbpool as a parameter
func PrintJsonHandler(writer http.ResponseWriter, requester *http.Request) {
	if requester.Method != http.MethodPost {
		http.Error(writer, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the body of the request
	body, err := io.ReadAll(requester.Body)
	if err != nil {
		http.Error(writer, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	defer requester.Body.Close()

	// Print the body to the console
	fmt.Printf("Received JSON body: %s\n", string(body))

	// Respond with a 200 OK
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("Received successfully"))
}

// Main JSON object
type GameData struct {
	Info     Info     `json:"info"`
	Metadata Metadata `json:"metadata"`
}

// Metadata object
type Metadata struct {
	MatchID      string   `json:"matchId"`
	Participants []string `json:"participants"`
}

// Info object
type Info struct {
	GameCreation       int64         `json:"gameCreation"`
	GameDuration       int           `json:"gameDuration"`
	GameEndTimestamp   int64         `json:"gameEndTimestamp"`
	GameStartTimestamp int64         `json:"gameStartTimestamp"`
	GameVersion        string        `json:"gameVersion"`
	GameID             int64         `json:"gameId"`
	Participants       []Participant `json:"participants"`
}

// Participant object
type Participant struct {
	Assists                       int    `json:"assists"`
	ChampExperience               int    `json:"champExperience"`
	ChampLevel                    int    `json:"champLevel"`
	ChampionID                    int    `json:"championId"`
	ChampionName                  string `json:"championName"`
	Deaths                        int    `json:"deaths"`
	GoldEarned                    int    `json:"goldEarned"`
	Item0                         string `json:"item0"`
	Item1                         string `json:"item1"`
	Item2                         string `json:"item2"`
	Item3                         string `json:"item3"`
	Item4                         string `json:"item4"`
	Item5                         string `json:"item5"`
	Item6                         string `json:"item6"`
	Kills                         int    `json:"kills"`
	NeutralMinionsKilled          int    `json:"neutralMinionsKilled"`
	Perks                         Perks  `json:"perks"`
	RiotIDGameName                string `json:"riotIdGameName"`
	RiotIDTagline                 string `json:"riotIdTagline"`
	Summoner1ID                   string `json:"summoner1Id"`
	Summoner2ID                   string `json:"summoner2Id"`
	SummonerName                  string `json:"summonerName"`
	TeamID                        int    `json:"teamId"`
	TotalAllyJungleMinionsKilled  int    `json:"totalAllyJungleMinionsKilled"`
	TotalDamageDealtToChampions   int    `json:"totalDamageDealtToChampions"`
	TotalEnemyJungleMinionsKilled int    `json:"totalEnemyJungleMinionsKilled"`
	TotalMinionsKilled            int    `json:"totalMinionsKilled"`
	VisionScore                   int    `json:"visionScore"`
	Win                           bool   `json:"win"`
}

// Perks object
type Perks struct {
	Styles []Style `json:"styles"`
}

// Style object
type Style struct {
	Selections []Selection `json:"selections,omitempty"`
	Style      string      `json:"style,omitempty"`
}

// Selection object
type Selection struct {
	Perk string `json:"perk"`
}

func GetGameTime(durationInSeconds int) string {
	minutes := durationInSeconds / 60
	seconds := durationInSeconds % 60
	formattedTime := fmt.Sprintf("%02d:%02d", minutes, seconds)
	return formattedTime
}

func UnixToDateString(epoch int64) string {
	// Convert the epoch time to a time.Time object
	t := time.Unix(epoch/1000, 0)
	// Format the time object as "year-month-day"
	return t.Format("2006-01-02")
}

func InsertIntoDatabase(writer http.ResponseWriter, requester *http.Request, sqliteDB *sql.DB) {
	if requester.Method != http.MethodPost {
		http.Error(writer, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	var body []byte
	var err error
	if requester.Header.Get("Content-Encoding") == "gzip" {
		// Decompress the gzip payload
		gr, err := gzip.NewReader(requester.Body)
		if err != nil {
			fmt.Println(writer, "Failed to create gzip reader", http.StatusInternalServerError)
			http.Error(writer, "Failed to create gzip reader", http.StatusInternalServerError)
			return
		}
		defer gr.Close()

		// Read the decompressed data
		body, err = io.ReadAll(gr)
		if err != nil {
			fmt.Println(writer, "Failed to read decompressed data", http.StatusInternalServerError)
			http.Error(writer, "Failed to read decompressed data", http.StatusInternalServerError)
			return
		}
	} else {
		// Read the body of the request
		body, err = io.ReadAll(requester.Body)
		if err != nil {
			http.Error(writer, "Failed to read request body", http.StatusInternalServerError)
			return
		}
		defer requester.Body.Close()
	}

	var rawJSON json.RawMessage
	err = json.Unmarshal(body, &rawJSON)
	if err != nil {
		fmt.Println("Raw Error parsing JSON:", err)
		return
	}

	var gameData GameData
	err = json.Unmarshal([]byte(rawJSON), &gameData)
	if err != nil {
		fmt.Println("Game Error parsing JSON here:", err)
		return
	}

	riotID := fmt.Sprintf("%s#%s", gameData.Info.Participants[0].RiotIDGameName, gameData.Info.Participants[0].RiotIDTagline)
	matchData, err := json.Marshal(gameData.Info.Participants)
	if err != nil {
		fmt.Println("Error marshalling struct:", err)
		return
	}

	participantData, err := json.Marshal(gameData.Metadata.Participants)
	if err != nil {
		fmt.Println("Error marshalling struct:", err)
		return
	}

	_, err = sqliteDB.Exec(
		`INSERT INTO "matchHistory" ("gameID", "gameVer", "riotID", "gameDurationMinutes", "gameCreationTimestamp", "gameEndTimestamp", "queueType", "gameDate", "participants", "matchData") VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT ("gameID", "riotID") DO NOTHING;`,
		gameData.Metadata.MatchID,
		gameData.Info.GameVersion,
		riotID,
		GetGameTime(gameData.Info.GameDuration),
		fmt.Sprintf("%d", gameData.Info.GameCreation),
		fmt.Sprintf("%d", gameData.Info.GameEndTimestamp),
		"Ranked Solo/Duo",
		UnixToDateString(gameData.Info.GameCreation),
		string(participantData),
		string(matchData),
	)

	// fmt.Println(gameData.Info.GameID)
	// fmt.Println(gameData.Info.GameVersion)
	// fmt.Println(riotID)
	// fmt.Println(GetGameTime(gameData.Info.GameDuration))
	// fmt.Println(gameData.Info.GameCreation)
	// fmt.Println(gameData.Info.GameEndTimestamp)
	// fmt.Println(UnixToDateString(gameData.Info.GameCreation))
	// fmt.Println(string(participantData))
	// fmt.Println(string(matchData))

	if err != nil {
		fmt.Println(err)
		http.Error(writer, "Database error", http.StatusInternalServerError)
		return
	}

	// Respond with a 200 OK
	writer.WriteHeader(http.StatusOK)
	// writer.Write([]byte("Inserted into database successfully"))
}
