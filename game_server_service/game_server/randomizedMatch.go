package gameServer

import (
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/noahpop77/Olympus/game_server_service/game_server/gameServerProto"
)

func generateRandomMatchData(matchID string, activeMatches *sync.Map, gameEndUnixTime int64, TeamOnePUUIDStruct []string, TeamTwoPUUIDStruct []string, rng *rand.Rand) {
    value, _ := activeMatches.Load(matchID)

    var participants *gameServerProto.MatchResult
    if value != nil {
        participants = value.(*gameServerProto.MatchResult)
    } else {
		participants = &gameServerProto.MatchResult{}
	}

	randomDuration := rng.Intn(300) + 60

	if participants.MatchID == "" {
		participants.MatchID = matchID
		participants.GameVersion = "14.21.630.3012"
		participants.GameDuration = strconv.Itoa(randomDuration)
		participants.GameStartTime = strconv.Itoa(int(gameEndUnixTime) - randomDuration)
		participants.GameEndTime = strconv.Itoa(int(gameEndUnixTime))
		participants.TeamOnePUUID = TeamOnePUUIDStruct
		participants.TeamTwoPUUID = TeamTwoPUUIDStruct

		activeMatches.Store(matchID, participants)
		return
	} else if participants.MatchID[:6] != "MATCH_" || len(participants.MatchID) < 7{
		fmt.Printf("Has other data: %s\n", participants.MatchID)
		return
	} else {
		return
	}
}

func generateGameData(match *gameServerProto.MatchCreation, matchParticipantsMap *sync.Map, matchDataMap *sync.Map) {

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	gameEndUnixTime := time.Now().Unix()
	itemIDs := getRandomItems(7)
	teamWin := (rng.Intn(2) == 0)
	var TeamOnePUUIDStruct []string
	var TeamTwoPUUIDStruct []string

	for i := 0; i < 10; i++{
		if i < 5 {
			TeamOnePUUIDStruct = append(TeamOnePUUIDStruct, match.ParticipantsPUUID[i])
		} else {
			TeamTwoPUUIDStruct = append(TeamTwoPUUIDStruct, match.ParticipantsPUUID[i])
			teamWin = !teamWin
		}
	}

	generateRandomMatchData(match.MatchID, matchDataMap, gameEndUnixTime,TeamOnePUUIDStruct, TeamTwoPUUIDStruct, rng)

	champID, champName := getRandomChamp(rng)

	addParticipant(match.MatchID, matchParticipantsMap, &gameServerProto.Participant{
		Assists:                       int32(rng.Intn(25)),
		ChampExperience:               int32(rng.Intn(12576)),
		ChampLevel:                    int32(rng.Intn(18) + 1),
		ChampionId:                    int32(champID), // Random champion id
		ChampionName:                  champName,
		Deaths:                        int32(rng.Intn(25)),
		GoldEarned:                    int32(rng.Intn(25000)),
		Item0:                         itemIDs[0],
		Item1:                         itemIDs[1],
		Item2:                         itemIDs[2],
		Item3:                         itemIDs[3],
		Item4:                         itemIDs[4],
		Item5:                         itemIDs[5],
		Item6:                         itemIDs[6],
		Kills:                         int32(rng.Intn(25)),
		NeutralMinionsKilled:          int32(rng.Intn(100)),
		Perks:                         &gameServerProto.Perks{
			Styles: []*gameServerProto.Style{
				{Selections: []*gameServerProto.Selection{
					{Perk: "9923"},
				}},
			},
		},
		RiotIdGameName:                fmt.Sprintf("RiotGameName%d", rng.Intn(100)),
		RiotIdTagline:                 RandomString(rng, 3),
		Summoner1Id:                   "12",
		Summoner2Id:                   "1",
		SummonerName:                  fmt.Sprintf("Xx%sLover%dxX", champName, rng.Intn(99999)),
		TeamId:                        int32(rng.Intn(2) * 100), // Team 100 or 200
		TotalAllyJungleMinionsKilled:  int32(rng.Intn(100)),
		TotalDamageDealtToChampions:   int32(rng.Intn(90000)),
		TotalEnemyJungleMinionsKilled: int32(rng.Intn(20)),
		TotalMinionsKilled:            int32(rng.Intn(200)),
		VisionScore:                   int32(rng.Intn(50)),
		Win:                           teamWin,
	})

}


func addParticipant(matchID string, matchParticipantsMap *sync.Map, participant *gameServerProto.Participant) {
    value, _ := matchParticipantsMap.Load(matchID)

    var participants []*gameServerProto.Participant
    if value != nil {
        participants = value.([]*gameServerProto.Participant)
    }

    participants = append(participants, participant)
    matchParticipantsMap.Store(matchID, participants)
}

// Based off of the letters string we can generate random strings based off a provided
// length and the characters provided
const letters = "1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
func RandomString(rng *rand.Rand, length int) string {
	result := make([]byte, length)
	for i := range result {
		result[i] = letters[rng.Intn(len(letters))]
	}
	return string(result)
}


func getRandomChamp(rng *rand.Rand) (int, string) {
	champions := map[string]string{
		"Aatrox":      "266",
		"Ahri":        "103",
		"Akali":       "84",
		"Akshan":      "166",
		"Alistar":     "12",
		"Ambessa":     "799",
		"Amumu":       "32",
		"Anivia":      "34",
		"Annie":       "1",
		"Aphelios":    "523",
		"Ashe":        "22",
		"AurelionSol": "136",
		"Aurora":      "893",
		"Azir":        "268",
		"Bard":        "432",
		"Belveth":     "200",
		"Blitzcrank":  "53",
		"Brand":       "63",
		"Braum":       "201",
		"Briar":       "233",
		"Caitlyn":     "51",
		"Camille":     "164",
		"Cassiopeia":  "69",
		"Chogath":     "31",
		"Corki":       "42",
		"Darius":      "122",
		"Diana":       "131",
		"Draven":      "119",
		"DrMundo":     "36",
		"Ekko":        "245",
		"Elise":       "60",
		"Evelynn":     "28",
		"Ezreal":      "81",
		"Fiddlesticks":"9",
		"Fiora":       "114",
		"Fizz":        "105",
		"Galio":       "3",
		"Gangplank":   "41",
		"Garen":       "86",
		"Gnar":        "150",
		"Gragas":      "79",
		"Graves":      "104",
		"Gwen":        "887",
		"Hecarim":     "120",
		"Heimerdinger":"74",
		"Hwei":        "910",
		"Illaoi":      "420",
		"Irelia":      "39",
		"Ivern":       "427",
		"Janna":       "40",
		"JarvanIV":    "59",
		"Jax":         "24",
		"Jayce":       "126",
		"Jhin":        "202",
		"Jinx":        "222",
		"Kaisa":       "145",
		"Kalista":     "429",
		"Karma":       "43",
		"Karthus":     "30",
		"Kassadin":    "38",
		"Katarina":    "55",
		"Kayle":       "10",
		"Kayn":        "141",
		"Kennen":      "85",
		"Khazix":      "121",
		"Kindred":     "203",
		"Kled":        "240",
		"KogMaw":      "96",
		"KSante":      "897",
		"Leblanc":     "7",
		"LeeSin":      "64",
		"Leona":       "89",
		"Lillia":      "876",
		"Lissandra":   "127",
		"Lucian":      "236",
		"Lulu":        "117",
		"Lux":         "99",
		"Malphite":    "54",
		"Malzahar":    "90",
		"Maokai":      "57",
		"MasterYi":    "11",
		"Milio":       "902",
		"MissFortune": "21",
		"MonkeyKing":  "62",
		"Mordekaiser": "82",
		"Morgana":     "25",
		"Naafiri":     "950",
		"Nami":        "267",
		"Nasus":       "75",
		"Nautilus":    "111",
		"Neeko":       "518",
		"Nidalee":     "76",
		"Nilah":       "895",
		"Nocturne":    "56",
		"Nunu":        "20",
		"Olaf":        "2",
		"Orianna":     "61",
		"Ornn":        "516",
		"Pantheon":    "80",
		"Poppy":       "78",
		"Pyke":        "555",
		"Qiyana":       "246",
		"Quinn":       "133",
		"Rakan":       "497",
		"Rammus":      "33",
		"RekSai":      "421",
		"Rell":        "526",
		"Renata":      "888",
		"Renekton":    "58",
		"Rengar":      "107",
		"Riven":       "92",
		"Rumble":      "68",
		"Ryze":        "13",
		"Samira":      "360",
		"Sejuani":     "113",
		"Senna":       "235",
		"Seraphine":   "147",
		"Sett":        "875",
		"Shaco":       "35",
		"Shen":        "98",
		"Shyvana":     "102",
		"Singed":      "27",
		"Sion":        "14",
		"Sivir":       "15",
		"Skarner":     "72",
		"Smolder":     "901",
		"Sona":        "37",
		"Soraka":      "16",
		"Swain":       "50",
		"Sylas":       "517",
		"Syndra":      "134",
		"TahmKench":   "223",
		"Taliyah":     "163",
		"Talon":       "91",
		"Taric":       "44",
		"Teemo":       "17",
		"Thresh":      "412",
		"Tristana":    "18",
		"Trundle":      "48",
		"Tryndamere":  "23",
		"TwistedFate": "4",
		"Twitch":      "29",
		"Udyr":        "77",
		"Urgot":       "6",
		"Varus":       "110",
		"Vayne":       "67",
		"Veigar":      "45",
		"Velkoz":      "161",
		"Vex":         "711",
		"Vi":          "254",
		"Viego":       "234",
		"Viktor":      "112",
		"Vladimir":    "8",
		"Volibear":    "106",
		"Warwick":     "19",
		"Xayah":       "498",
		"Xerath":      "101",
		"XinZhao":     "5",
		"Yasuo":       "157",
		"Yone":        "777",
		"Yorick":      "83",
		"Yuumi":       "350",
		"Zac":         "154",
		"Zed":         "238",
		"Zeri":        "221",
		"Ziggs":       "115",
		"Zilean":      "26",
		"Zoe":         "142",
		"Zyra":        "143",
	}

	// Pick a random champion from the list
	var keys []string
	for key := range champions {
		keys = append(keys, key)
	}
	randomChampion := keys[rng.Intn(len(keys))]
	randomChampionID := champions[randomChampion]

	champID, _ := strconv.Atoi(randomChampionID)

	return champID, randomChampion
}

// Returns the specified amount of random item codes
func getRandomItems(numItemsNeeded int) []string {

	itemIDs := []string{
		"0", "1001", "1004", "1006", "1011", "1018", "1026", "1027", "1028", "1029", "1031", "1033", "1035", "1036", "1037", "1038", "1039", "1040", "1042", "1043", "1052", "1053", "1054", "1055", "1056", "1057", "1058", "1082", "1083", "1101", "1102", "1103", "1104", "1500", "1501", "1502", "1503", "1504", "1506", "1507", "1508", "1509", "1510", "1511", "1512", "1515", "1516", "1517", "1518", "1519", "1520", "1521", "1522", "2003", "2010", "2015", "2019", "2020", "2021", "2022", "2031", "2033", "2049", "2050", "2051", "2052", "2055", "2056", "2065", "2138", "2139", "2140", "2141", "2142", "2143", "2144", "2150", "2151", "2152", "2403", "2419", "2420", "2421", "2422", "2423", "2502", "2504", "3001", "3002", "3003", "3004", "3005", "3006", "3009", "3011", "3012", "3020", "3023", "3024", "3026", "3031", "3033", "3035", "3036", "3039", "3040", "3041", "3042", "3044", "3046", "3047", "3050", "3051", "3053", "3057", "3065", "3066", "3067", "3068", "3070", "3071", "3072", "3073", "3074", "3075", "3076", "3077", "3078", "3082", "3083", "3084", "3085", "3086", "3087", "3089", "3091", "3094", "3095", "3100", "3102", "3105", "3107", "3108", "3109", "3110", "3111", "3112", "3113", "3114", "3115", "3116", "3117", "3118", "3119", "3121", "3123", "3124", "3128", "3131", "3133", "3134", "3135", "3137", "3139", "3140", "3142", "3143", "3145", "3146", "3147", "3152", "3153", "3155", "3156", "3157", "3158", "3161", "3165", "3172", "3177", "3179", "3181", "3184", "3190", "3191", "3193", "3211", "3222", "3302", "3330", "3340", "3348", "3349", "3363", "3364", "3400", "3430", "3504", "3508", "3513", "3599", "3600", "3742", "3748", "3801", "3802", "3803", "3814", "3850", "3851", "3853", "3854", "3855", "3857", "3858", "3859", "3860", "3862", "3863", "3864", "3865", "3866", "3867", "3869", "3870", "3871", "3876", "3877", "3901", "3902", "3903", "3916", "4003", "4004", "4005", "4010", "4011", "4012", "4013", "4014", "4015", "4016", "4017", "4401", "4402", "4403", "4628", "4629", "4630", "4632", "4633", "4635", "4636", "4637", "4638", "4641", "4642", "4643", "4644", "4645", "4646", "6029", "6035", "6333", "6609", "6610", "6616", "6617", "6620", "6621", "6630", "6631", "6632", "6653", "6655", "6656", "6657", "6660", "6662", "6664", "6665", "6667", "6670", "6671", "6672", "6673", "6675", "6676", "6677", "6690", "6691", "6692", "6693", "6694", "6695", "6696", "6697", "6698", "6699", "6700", "6701", "7000", "7001", "7002", "7003", "7004", "7005", "7006", "7007", "7008", "7009", "7010", "7011", "7012", "7013", "7014", "7015", "7016", "7017", "7018", "7019", "7020", "7021", "7022", "7023", "7024", "7025", "7026", "7027", "7028", "7029", "7030", "7031", "7032", "7033", "7034", "7035", "7036", "7037", "7038", "7039", "7040", "7041", "7042", "7050", "8001", "8020", "221038", "221053", "221058", "222051", "222065", "223001", "223003", "223004", "223005", "223006", "223009", "223011", "223020", "223026", "223031", "223033", "223036", "223039", "223040", "223042", "223046", "223047", "223050", "223053", "223057", "223065", "223067", "223068", "223071", "223072", "223074", "223075", "223078", "223084", "223085", "223087", "223089", "223091", "223094", "223095", "223100", "223102", "223105", "223107", "223109", "223110", "223111", "223112", "223115", "223116", "223119", "223121", "223124", "223135", "223139", "223142", "223143", "223146", "223152", "223153", "223156", "223157", "223158", "223161", "223165", "223172", "223177", "223181", "223184", "223185", "223190", "223193", "223222", "223504", "223508", "223742", "223748", "223814", "224004", "224005", "224401", "224403", "224628", "224629", "224633", "224636", "224637", "224644", "224645", "226035", "226333", "226609", "226616", "226617", "226620", "226630", "226631", "226632", "226653", "226655", "226656", "226657", "226662", "226664", "226665", "226667", "226671", "226672", "226673", "226675", "226676", "226691", "226692", "226693", "226694", "226695", "226696", "227001", "227002", "227005", "227006", "227009", "227010", "227011", "227012", "227013", "227014", "227015", "227016", "227017", "227018", "227019", "227020", "227021", "227023", "227024", "227025", "227026", "227027", "227028", "227029", "227030", "227031", "227032", "227033", "228001", "228002", "228003", "228004", "228005", "228006", "228008", "228020",
	}

	// Seed random number generator
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Pick 7 random item IDs
	var randomIDs []string
	for len(randomIDs) < numItemsNeeded {
		randomIndex := rng.Intn(len(itemIDs))
		randomID := itemIDs[randomIndex]

		// Ensure no duplicates by checking if the ID has already been picked
		if !contains(randomIDs, randomID) {
			randomIDs = append(randomIDs, randomID)
		}
	}

	return randomIDs
}

// Helper function used in getRandomItems to ensure unique items per match 
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}