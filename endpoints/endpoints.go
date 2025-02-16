package endpoints

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof" // Import pprof package
	"time"

	"github.com/redis/go-redis/v9"
)

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

func InsertIntoDatabase(writer http.ResponseWriter, requester *http.Request, rdb *redis.Client, ctx context.Context) {
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

	val, err := rdb.Get(ctx, gameData.Metadata.MatchID).Result()
	if err != nil && err != redis.Nil {
		panic(err)
	}
	
	if err == redis.Nil {
		//string(body) is the stringified JSON payload sent to the endpoint
		err = rdb.Set(ctx, gameData.Metadata.MatchID, string(body), 0).Err()
		if err != nil {
			panic(err)
		}
		fmt.Println("Redis Cache Miss")
		writer.WriteHeader(http.StatusOK)
		writer.Write(body)
		return
	} else {
		fmt.Println("Redis Cache Hit")
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(val))
		return
	}
}
