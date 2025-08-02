// Filename: internal/handlers/websocket.go
package handlers

import (
	"encoding/json"
	"fmt"
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
	"geoguessr-backend/internal/utils"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// PlayerInfo stores basic WebSocket connection-related info for a player
type PlayerInfo struct {
	Conn     *websocket.Conn `json:"-"` // The WebSocket connection itself
	Username string          `json:"username"`
	UserID   uint            `json:"userId"`
	IsHost   bool            `json:"isHost"`
	IsReady  bool            `json:"isReady"`
}

// PlayerDetailInfo is used for payloads sent to clients, including more game-specific state
type PlayerDetailInfo struct {
	UserID        uint   `json:"userId"`
	Username      string `json:"username"`
	IsHost        bool   `json:"isHost"`
	IsReady       bool   `json:"isReady"`
	CurrentHealth int    `json:"currentHealth"`
	Status        string `json:"status"` // e.g., "active", "eliminated", "disconnected"
	TotalScore    int    `json:"totalScore"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		allowedOrigins := []string{"http://localhost:5173", "https://rudra-garg.github.io"} // Add your deployed frontend origin here
		for _, allowed := range allowedOrigins {
			if origin == allowed {
				return true
			}
		}
		log.Printf("WebSocket CheckOrigin: Denied origin: %s", origin)
		return false
	},
}

var gameRooms = struct {
	sync.RWMutex
	rooms map[string]map[uint]*PlayerInfo // gameCode -> userID -> PlayerInfo
}{
	rooms: make(map[string]map[uint]*PlayerInfo),
}

// Message Types
const (
	MsgTypePlayerJoin             = "player_join"
	MsgTypePlayerLeave            = "player_leave"
	MsgTypeGameStart              = "game_start"
	MsgTypeRoundStart             = "round_start"
	MsgTypeGuessSubmit            = "guess_submit"
	MsgTypeRoundEnd               = "round_end"
	MsgTypeGameEnd                = "game_end"
	MsgTypePlayerReady            = "player_ready"
	MsgTypeChatMessage            = "chat_message"
	MsgTypePlayerListUpdate       = "player_list_update"
	MsgTypeError                  = "error"
	MsgTypeFirstPlayerGuessed     = "first_player_guessed"
	MsgTypeHostProceedToNextRound = "host_proceed_next_round" // New message type
	// MsgTypeRoundTimerUpdate   = "round_timer_update" // Currently not used, timer logic is on first guess
)

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	GameID  string          `json:"gameId"`
	UserID  uint            `json:"userId,omitempty"`
}

var activeRoundTimers = struct {
	sync.RWMutex
	timers map[uint]*time.Timer
}{
	timers: make(map[uint]*time.Timer),
}

var roundProcessingLocks = struct {
	sync.RWMutex
	locks map[string]struct{}
}{
	locks: make(map[string]struct{}),
}

// --- Calculation Helpers ---
func calculateDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371 // Radius of the Earth in km
	dLat := deg2rad(lat2 - lat1)
	dLon := deg2rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(deg2rad(lat1))*math.Cos(deg2rad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func deg2rad(deg float64) float64 {
	return deg * (math.Pi / 180)
}

func calculateScore(distanceKm float64) int {
	const maxScore = 5000
	if distanceKm < 0 {
		distanceKm = 0
	}
	if distanceKm > 20000 {
		return 0
	}
	score := math.Round(maxScore * math.Exp(-distanceKm/2000))
	return int(math.Max(0, score))
}

// --- WebSocket Connection Handler ---
func HandleWebSocket(c *gin.Context) {
	gameCode := c.Query("gameCode")
	tokenString := c.Query("token")
	log.Printf("WS HandleWebSocket: Attempting connection for GameCode=%s, Token provided: %t", gameCode, tokenString != "")

	if gameCode == "" || tokenString == "" {
		log.Println("WS Error: Missing gameCode or token query parameter")
		sendErrorHTTP(c, http.StatusBadRequest, "gameCode and token are required")
		return
	}

	token, err := utils.ValidateToken(tokenString)
	if err != nil {
		log.Printf("WS Error: Invalid token for GameCode=%s: %v", gameCode, err)
		sendErrorHTTP(c, http.StatusUnauthorized, "Invalid or expired token")
		return
	}
	userID, err := utils.ExtractUserIDFromToken(token)
	if err != nil {
		log.Printf("WS Error: Cannot extract userID from token for GameCode=%s: %v", gameCode, err)
		sendErrorHTTP(c, http.StatusUnauthorized, "Invalid token claims")
		return
	}

	db := database.GetDB()
	var user models.User
	if err := db.First(&user, userID).Error; err != nil {
		log.Printf("WS Error: User %d not found in DB (GameCode=%s): %v", userID, gameCode, err)
		sendErrorHTTP(c, http.StatusNotFound, "Authenticated user not found")
		return
	}

	var game models.MultiplayerGame
	if err := db.Where("game_code = ?", gameCode).First(&game).Error; err != nil {
		log.Printf("WS Error: Game %s not found in DB for user %d: %v", gameCode, userID, err)
		sendErrorHTTP(c, http.StatusNotFound, "Game not found")
		return
	}

	var session models.MultiplayerSession
	err = db.Transaction(func(tx *gorm.DB) error {
		if errTx := tx.Preload("User").Where("game_id = ? AND user_id = ?", game.ID, userID).First(&session).Error; errTx != nil {
			if errTx == gorm.ErrRecordNotFound {
				return fmt.Errorf("user is not part of this game session")
			}
			return fmt.Errorf("database error finding session: %w", errTx)
		}
		if !session.IsActive {
			if session.Status == "eliminated" || session.CurrentHealth <= 0 {
				log.Printf("WS Info: User %d (Game %s) attempted to rejoin but is eliminated (Health: %d, Status: %s).", userID, gameCode, session.CurrentHealth, session.Status)
				return fmt.Errorf("player is eliminated and cannot rejoin actively")
			}
			session.IsActive = true
			session.LeftAt = nil
			if errTx := tx.Save(&session).Error; errTx != nil {
				return fmt.Errorf("failed to reactivate session: %w", errTx)
			}
			log.Printf("WS Info: Reactivated session for User %d in Game %s. Health: %d", userID, gameCode, session.CurrentHealth)
		} else if session.Status == "eliminated" || session.CurrentHealth <= 0 {
			log.Printf("WS Info: User %d (Game %s) connected as eliminated (Health: %d, Status: %s).", userID, gameCode, session.CurrentHealth, session.Status)
		}
		return nil
	})

	if err != nil {
		log.Printf("WS Error: Session validation/reactivation for User %d, Game %s: %v", userID, gameCode, err)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "user is not part of") || strings.Contains(err.Error(), "eliminated") {
			status = http.StatusForbidden
		}
		sendErrorHTTP(c, status, err.Error())
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WS Upgrade Error for game %s, user %d: %v", gameCode, userID, err)
		return
	}
	defer conn.Close()
	log.Printf("WS SUCCESS: UserID=%d (%s) connected to GameCode=%s. RemoteAddr=%s. Session Status: %s, Health: %d",
		userID, user.Username, gameCode, conn.RemoteAddr(), session.Status, session.CurrentHealth)

	playerInfo := &PlayerInfo{
		Conn:     conn,
		Username: user.Username,
		UserID:   userID,
		IsHost:   session.IsHost,
		IsReady:  session.IsReady,
	}

	gameRooms.Lock()
	if _, ok := gameRooms.rooms[gameCode]; !ok {
		gameRooms.rooms[gameCode] = make(map[uint]*PlayerInfo)
	}
	gameRooms.rooms[gameCode][userID] = playerInfo
	gameRooms.Unlock()

	currentPlayersList := getCurrentPlayersInfoWithDetails(gameCode, game.ID)
	log.Printf("WS SEND: Initial detailed player list (%d players) to UserID %d for GameCode %s", len(currentPlayersList), userID, gameCode)
	if err := sendDetailedPlayerListUpdate(conn, gameCode, currentPlayersList); err != nil {
		log.Printf("WS ERROR: Failed to send initial detailed player list to User %d: %v", userID, err)
		return
	}
	broadcastDetailedPlayerListUpdate(gameCode, currentPlayersList, conn)

	defer func() {
		log.Printf("WS INFO: Starting disconnect cleanup for UserID %d, GameCode %s", userID, gameCode)
		var hostChanged bool = false

		gameRooms.Lock()
		room, roomExists := gameRooms.rooms[gameCode]
		if roomExists {
			if pInfo, ok := room[userID]; ok && pInfo.Conn == conn {
				originalHostStatus := pInfo.IsHost
				delete(room, userID)
				remainingCount := len(room)
				log.Printf("WS INFO: Removed UserID %d from room %s. Remaining: %d", userID, gameCode, remainingCount)

				go database.GetDB().Model(&models.MultiplayerSession{}).Where("game_id = ? AND user_id = ?", game.ID, userID).Updates(map[string]interface{}{"is_active": false, "left_at": time.Now()})

				if remainingCount == 0 {
					delete(gameRooms.rooms, gameCode)
					log.Printf("WS INFO: Game room %s closed (empty). Attempting to clean up timer.", gameCode)
					activeRoundTimers.Lock()
					if timer, ok := activeRoundTimers.timers[game.ID]; ok {
						timer.Stop()
						delete(activeRoundTimers.timers, game.ID)
						log.Printf("WS CLEANUP: Stopped and removed timer for game %d as room became empty.", game.ID)
					}
					activeRoundTimers.Unlock()
					currentRoundKey := fmt.Sprintf("%d-%d", game.ID, game.CurrentRound) // Use game.CurrentRound at time of disconnect
					roundProcessingLocks.Lock()
					delete(roundProcessingLocks.locks, currentRoundKey)
					roundProcessingLocks.Unlock()
				} else if originalHostStatus {
					hostChanged = assignNewHost(room, game.ID, playerInfo)
				}
			}
		}
		gameRooms.Unlock()

		if roomExists && len(gameRooms.rooms[gameCode]) > 0 {
			currentPlayersAfterLeave := getCurrentPlayersInfoWithDetails(gameCode, game.ID)
			broadcastDetailedPlayerListUpdate(gameCode, currentPlayersAfterLeave, nil)
		}
		log.Printf("WS INFO: Finished disconnect cleanup for UserID %d, GameCode %s. Host changed: %t", userID, gameCode, hostChanged)
	}()

	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("WS Read Error (User %d, Game %s): %v", userID, gameCode, err)
			} else if err == websocket.ErrCloseSent || strings.Contains(err.Error(), "websocket: close 1000") {
				log.Printf("WS INFO: Clean WebSocket close detected for User %d, Game %s.", userID, gameCode)
			} else {
				log.Printf("WS INFO: WebSocket connection closed for User %d, Game %s: %v", userID, gameCode, err)
			}
			break
		}

		msg.UserID = userID
		log.Printf("WS RECV: User %d (Game %s) | Type: %s | Payload: %s", userID, gameCode, msg.Type, string(msg.Payload))

		var currentSessionState models.MultiplayerSession
		db.Where("game_id = ? AND user_id = ?", game.ID, userID).First(&currentSessionState)

		if currentSessionState.Status == "eliminated" && msg.Type != MsgTypeChatMessage {
			log.Printf("WS WARN: User %d is eliminated and tried to send action: %s", userID, msg.Type)
			sendErrorMessage(conn, "You are eliminated and cannot perform this action.")
			continue
		}
		handleWSMessage(msg, playerInfo, gameCode, game.ID)
	}
}

func getCurrentPlayersInfoWithDetails(gameCode string, gameDBID uint) []*PlayerDetailInfo {
	db := database.GetDB()
	var sessions []models.MultiplayerSession
	if err := db.Preload("User").Where("game_id = ?", gameDBID).Order("is_host DESC, is_active DESC, joined_at ASC").Find(&sessions).Error; err != nil {
		log.Printf("WS ERROR: Failed to fetch sessions for GameDBID %d for detailed player list: %v", gameDBID, err)
		return []*PlayerDetailInfo{}
	}

	detailedPlayers := make([]*PlayerDetailInfo, 0, len(sessions))
	for _, s := range sessions {
		gameRooms.RLock()
		_, connected := gameRooms.rooms[gameCode][s.UserID]
		gameRooms.RUnlock()

		username := s.User.Username
		if username == "" {
			username = fmt.Sprintf("User %d", s.UserID)
		}

		displayStatus := s.Status
		if !s.IsActive && s.Status == "active" {
			displayStatus = "disconnected"
		} else if !connected && s.IsActive && s.Status == "active" {
			displayStatus = "disconnected_ws"
		}

		pi := PlayerDetailInfo{
			UserID:        s.UserID,
			Username:      username,
			IsHost:        s.IsHost,
			IsReady:       s.IsReady,
			CurrentHealth: s.CurrentHealth,
			Status:        displayStatus,
			TotalScore:    s.TotalScore,
		}
		detailedPlayers = append(detailedPlayers, &pi)
	}
	return detailedPlayers
}

func sendDetailedPlayerListUpdate(conn *websocket.Conn, gameCode string, playersInfo []*PlayerDetailInfo) error {
	if conn == nil {
		return fmt.Errorf("nil connection")
	}
	payloadBytes, err := json.Marshal(playersInfo)
	if err != nil {
		log.Printf("WS ERROR: Marshalling detailed player list for %s failed: %v", gameCode, err)
		return err
	}
	msg := WSMessage{Type: MsgTypePlayerListUpdate, Payload: json.RawMessage(payloadBytes), GameID: gameCode}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Printf("SetWriteDeadline error: %v", err)
	}
	defer conn.SetWriteDeadline(time.Time{})
	return conn.WriteJSON(msg)
}

func broadcastDetailedPlayerListUpdate(gameCode string, playersInfo []*PlayerDetailInfo, excludeConn *websocket.Conn) {
	payloadBytes, err := json.Marshal(playersInfo)
	if err != nil {
		log.Printf("WS ERROR: Marshalling detailed player list broadcast for %s failed: %v", gameCode, err)
		return
	}
	msg := WSMessage{Type: MsgTypePlayerListUpdate, Payload: json.RawMessage(payloadBytes), GameID: gameCode}
	broadcastWSMessage(gameCode, msg, excludeConn)
}

func assignNewHost(room map[uint]*PlayerInfo, gameDBID uint, disconnectedHostInfo *PlayerInfo) bool {
	db := database.GetDB()
	var potentialNewHostID uint
	var earliestJoinTime time.Time = time.Now().Add(24 * time.Hour * 365) // Initialize with a future time

	for userID := range room {
		var session models.MultiplayerSession
		if err := db.Where("game_id = ? AND user_id = ? AND is_active = true AND status = 'active'", gameDBID, userID).First(&session).Error; err == nil {
			if potentialNewHostID == 0 || session.JoinedAt.Before(earliestJoinTime) {
				potentialNewHostID = userID
				earliestJoinTime = session.JoinedAt
			}
		}
	}

	if potentialNewHostID != 0 {
		if pInfo, ok := room[potentialNewHostID]; ok {
			pInfo.IsHost = true
		}
		log.Printf("WS INFO: Assigning new host UserID %d for GameDBID %d in memory.", potentialNewHostID, gameDBID)

		go func(gID uint, oldHostUID uint, newHostUID uint) {
			tx := database.GetDB().Begin()
			defer func() {
				if r := recover(); r != nil {
					tx.Rollback()
					log.Printf("WS RECOVERED: Panic during host reassignment DB update for game %d. Rolled back. %v", gID, r)
				}
			}()

			if err := tx.Model(&models.MultiplayerSession{}).Where("game_id = ? AND user_id = ?", gID, oldHostUID).Update("is_host", false).Error; err != nil {
				log.Printf("WS ERROR: DB: Failed to unset old host %d for game %d: %v", oldHostUID, gID, err)
				tx.Rollback()
				return
			}
			if err := tx.Model(&models.MultiplayerSession{}).Where("game_id = ? AND user_id = ?", gID, newHostUID).Update("is_host", true).Error; err != nil {
				log.Printf("WS ERROR: DB: Failed to set new host %d for game %d: %v", newHostUID, gID, err)
				tx.Rollback()
				return
			}
			if err := tx.Model(&models.MultiplayerGame{}).Where("id = ?", gID).Update("host_user_id", newHostUID).Error; err != nil {
				log.Printf("WS ERROR: DB: Failed to update game's host_user_id for game %d: %v", gID, err)
				tx.Rollback()
				return
			}
			if err := tx.Commit().Error; err != nil {
				log.Printf("WS ERROR: DB: Failed to commit host reassignment for game %d: %v", gID, err)
				return
			}
			log.Printf("WS INFO: DB: Successfully reassigned host from %d to %d for game %d.", oldHostUID, newHostUID, gID)
		}(gameDBID, disconnectedHostInfo.UserID, potentialNewHostID)
		return true
	}
	log.Printf("WS WARN: Could not assign new host for GameDBID %d. No suitable candidates.", gameDBID)
	return false
}

func sendErrorMessage(conn *websocket.Conn, errorMsg string) {
	if conn == nil {
		return
	}
	payload := gin.H{"message": errorMsg}
	payloadBytes, _ := json.Marshal(payload)
	msg := WSMessage{Type: MsgTypeError, Payload: json.RawMessage(payloadBytes)}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		log.Printf("SetWriteDeadline error: %v", err)
	}
	defer conn.SetWriteDeadline(time.Time{})
	if err := conn.WriteJSON(msg); err != nil {
		log.Printf("WS ERROR: Failed to send error JSON to client: %v", err)
	}
}

func sendErrorHTTP(c *gin.Context, statusCode int, message string) {
	if !c.Writer.Written() {
		c.AbortWithStatusJSON(statusCode, gin.H{"error": message})
	} else {
		log.Printf("WS/HTTP Error: Attempted to send HTTP error but headers already written. Status: %d, Msg: %s", statusCode, message)
	}
}

func broadcastWSMessage(gameCode string, message WSMessage, excludeConn *websocket.Conn) {
	gameRooms.RLock()
	room, exists := gameRooms.rooms[gameCode]
	if !exists {
		gameRooms.RUnlock()
		return
	}
	var connsToBroadcast []*websocket.Conn
	for _, player := range room {
		if player.Conn != excludeConn {
			connsToBroadcast = append(connsToBroadcast, player.Conn)
		}
	}
	gameRooms.RUnlock()

	for _, conn := range connsToBroadcast {
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			log.Printf("SetWriteDeadline error on broadcast: %v", err)
		}
		err := conn.WriteJSON(message)
		conn.SetWriteDeadline(time.Time{}) // Reset deadline immediately
		if err != nil {
			log.Printf("WS ERROR: Broadcasting message type %s to a client in game %s failed: %v. Conn RemoteAddr: %s", message.Type, gameCode, err, conn.RemoteAddr())
			// Potentially handle client cleanup here if WriteJSON fails consistently
		}
	}
}

func broadcastChatMessage(gameCode string, payload json.RawMessage, sender *PlayerInfo) {
	msg := WSMessage{Type: MsgTypeChatMessage, Payload: payload, GameID: gameCode, UserID: sender.UserID}
	broadcastWSMessage(gameCode, msg, nil)
	log.Printf("WS BROADCAST: Chat message from User %d in game %s", sender.UserID, gameCode)
}

var activeRoundGuesses = struct {
	sync.RWMutex
	guesses map[uint]map[int]map[uint]bool
}{
	guesses: make(map[uint]map[int]map[uint]bool),
}

func recordPlayerGuessForRound(gameDBID uint, roundNumber int, userID uint) {
	activeRoundGuesses.Lock()
	defer activeRoundGuesses.Unlock()
	if _, ok := activeRoundGuesses.guesses[gameDBID]; !ok {
		activeRoundGuesses.guesses[gameDBID] = make(map[int]map[uint]bool)
	}
	if _, ok := activeRoundGuesses.guesses[gameDBID][roundNumber]; !ok {
		activeRoundGuesses.guesses[gameDBID][roundNumber] = make(map[uint]bool)
	}
	activeRoundGuesses.guesses[gameDBID][roundNumber][userID] = true
}

func clearRoundGuesses(gameDBID uint, roundNumber int) {
	activeRoundGuesses.Lock()
	defer activeRoundGuesses.Unlock()
	if gameRounds, ok := activeRoundGuesses.guesses[gameDBID]; ok {
		delete(gameRounds, roundNumber)
		if len(gameRounds) == 0 {
			delete(activeRoundGuesses.guesses, gameDBID)
		}
	}
}

func clearAllGuessesForGame(gameDBID uint) {
	activeRoundGuesses.Lock()
	defer activeRoundGuesses.Unlock()
	delete(activeRoundGuesses.guesses, gameDBID)
}

func checkAllActivePlayersGuessed(db *gorm.DB, gameDBID uint, roundNumber int) (bool, []uint, error) {
	var activeNonEliminatedSessions []models.MultiplayerSession
	err := db.Where("game_id = ? AND is_active = true AND status = 'active'", gameDBID).Find(&activeNonEliminatedSessions).Error
	if err != nil {
		return false, nil, fmt.Errorf("failed to fetch active player sessions: %w", err)
	}

	if len(activeNonEliminatedSessions) == 0 {
		log.Printf("WS CheckAllGuessed: No active, non-eliminated players for game %d, round %d.", gameDBID, roundNumber)
		return true, []uint{}, nil
	}

	activeRoundGuesses.RLock()
	gameSessionGuesses, gameExists := activeRoundGuesses.guesses[gameDBID]
	var roundPlayerGuesses map[uint]bool
	var roundExists bool
	if gameExists {
		roundPlayerGuesses, roundExists = gameSessionGuesses[roundNumber]
	}
	activeRoundGuesses.RUnlock()

	if !gameExists || !roundExists {
		log.Printf("WS CheckAllGuessed: Guess tracker empty/not found for game %d, round %d. Not all guessed.", gameDBID, roundNumber)
		return false, nil, nil
	}

	var playersWhoGuessed []uint
	allGuessed := true
	for _, session := range activeNonEliminatedSessions {
		if guessed, ok := roundPlayerGuesses[session.UserID]; ok && guessed {
			playersWhoGuessed = append(playersWhoGuessed, session.UserID)
		} else {
			allGuessed = false
		}
	}
	log.Printf("WS CheckAllGuessed: Game %d, Round %d. AllGuessed: %t. Guessed count: %d/%d", gameDBID, roundNumber, allGuessed, len(playersWhoGuessed), len(activeNonEliminatedSessions))
	return allGuessed, playersWhoGuessed, nil
}

func tryProcessRoundCompletion(gameDBID uint, gameCode string, roundNumber int, db *gorm.DB) {
	roundKey := fmt.Sprintf("%d-%d", gameDBID, roundNumber)

	roundProcessingLocks.Lock()
	if _, processing := roundProcessingLocks.locks[roundKey]; processing {
		roundProcessingLocks.Unlock()
		log.Printf("WS INFO: Round %d for game %d already being processed or was recently processed.", roundNumber, gameDBID)
		return
	}
	roundProcessingLocks.locks[roundKey] = struct{}{}
	roundProcessingLocks.Unlock()

	activeRoundTimers.Lock()
	if timer, ok := activeRoundTimers.timers[gameDBID]; ok {
		log.Printf("WS TRY_PROC_ROUND_END: Stopping timer for game %d, round %d.", gameDBID, roundNumber)
		timer.Stop()
		delete(activeRoundTimers.timers, gameDBID)
	}
	activeRoundTimers.Unlock()

	log.Printf("WS TRY_PROC_ROUND_END: Proceeding to processRoundCompletion for game %d, round %d.", gameDBID, roundNumber)
	processRoundCompletion(gameDBID, gameCode, roundNumber) // Removed 'go' to process synchronously if needed by timer
}

func processRoundCompletion(gameDBID uint, gameCode string, currentRoundNumber int) {
	db := database.GetDB()
	log.Printf("WS PROC_ROUND_END: Game %d (%s), Round %d completion processing started...", gameDBID, gameCode, currentRoundNumber)

	defer func() { // Ensure lock is always released
		roundProcessingLocks.Lock()
		delete(roundProcessingLocks.locks, fmt.Sprintf("%d-%d", gameDBID, currentRoundNumber))
		roundProcessingLocks.Unlock()
	}()

	var game models.MultiplayerGame
	if err := db.First(&game, gameDBID).Error; err != nil {
		log.Printf("WS ERROR PROC_ROUND_END: Game %d fetch failed: %v", gameDBID, err)
		return
	}

	var roundGuessesFromDB []models.MultiplayerRound
	err := db.Joins("JOIN multiplayer_sessions ON multiplayer_sessions.id = multiplayer_rounds.session_id").
		Where("multiplayer_sessions.game_id = ? AND multiplayer_rounds.round_number = ?", gameDBID, currentRoundNumber).
		Preload("Session.User").
		Order("multiplayer_rounds.score DESC").
		Find(&roundGuessesFromDB).Error
	if err != nil {
		log.Printf("WS ERROR PROC_ROUND_END: Fetching round guesses for Game %d, Round %d failed: %v", gameDBID, currentRoundNumber, err)
		return
	}

	var roundWinnerSession models.MultiplayerSession
	winnerScore := 0
	if len(roundGuessesFromDB) > 0 {
		roundWinnerSession = roundGuessesFromDB[0].Session
		winnerScore = roundGuessesFromDB[0].Score
		log.Printf("WS PROC_ROUND_END: Game %d, Round %d Winner: UserID %d (%s), Score: %d", gameDBID, currentRoundNumber, roundWinnerSession.UserID, roundWinnerSession.User.Username, winnerScore)
	} else {
		log.Printf("WS PROC_ROUND_END: Game %d, Round %d. No guesses submitted. Max possible score (for damage) is 0.", gameDBID, currentRoundNumber)
	}

	damageMultiplier := 1.0
	var playerRoundResultsForPayload []map[string]interface{}
	sessionsToUpdate := make(map[uint]*models.MultiplayerSession)

	var allGameSessions []models.MultiplayerSession
	if err := db.Preload("User").Where("game_id = ?", gameDBID).Find(&allGameSessions).Error; err != nil {
		log.Printf("WS ERROR PROC_ROUND_END: Failed to fetch all sessions for game %d: %v", gameDBID, err)
		return
	}
	sessionMap := make(map[uint]*models.MultiplayerSession)
	for i := range allGameSessions {
		sessionMap[allGameSessions[i].UserID] = &allGameSessions[i]
	}

	guessedPlayerIDs := make(map[uint]models.MultiplayerRound)
	for _, rg := range roundGuessesFromDB {
		guessedPlayerIDs[rg.Session.UserID] = rg
	}

	for _, session := range allGameSessions {
		if session.Status == "eliminated" || !session.IsActive {
			if _, wasInMap := sessionMap[session.UserID]; wasInMap {
				playerRoundResultsForPayload = append(playerRoundResultsForPayload, map[string]interface{}{
					"userId": session.UserID, "username": session.User.Username, "guess": nil, "distanceKm": -1.0,
					"roundScore": 0, "damageTaken": 0, "currentHealth": session.CurrentHealth, "status": session.Status, "totalScore": session.TotalScore,
				})
			}
			continue
		}
		sessionToUpdate := sessionMap[session.UserID]
		damageTaken := 0
		playerGuessData, playerGuessedThisRound := guessedPlayerIDs[session.UserID]

		if playerGuessedThisRound {
			if roundWinnerSession.ID == 0 || session.UserID != roundWinnerSession.UserID {
				scoreDifference := winnerScore - playerGuessData.Score
				if scoreDifference < 0 {
					scoreDifference = 0
				}
				damageTaken = int(float64(scoreDifference) * damageMultiplier)
			}
		} else {
			damageTaken = int(float64(winnerScore) * damageMultiplier) // Damage is based on winner's score if player didn't guess
			if winnerScore == 0 && len(roundGuessesFromDB) == 0 {      // If no one guessed, apply fixed penalty
				damageTaken = game.InitialHealth / game.RoundsTotal / 2 // Example: 1/10th of initial health for not guessing if no one scores
				if damageTaken == 0 {
					damageTaken = 100
				} // Minimum penalty
			}
			log.Printf("WS PROC_ROUND_END: UserID %d (%s) did not guess. Applying damage: %d.", session.UserID, session.User.Username, damageTaken)
		}

		if damageTaken > 0 {
			originalHealth := sessionToUpdate.CurrentHealth
			sessionToUpdate.CurrentHealth -= damageTaken
			log.Printf("WS PROC_ROUND_END: UserID %d health %d -> %d (damage %d)", sessionToUpdate.UserID, originalHealth, sessionToUpdate.CurrentHealth, damageTaken)
		}
		if sessionToUpdate.CurrentHealth <= 0 {
			sessionToUpdate.CurrentHealth = 0
			sessionToUpdate.Status = "eliminated"
			sessionToUpdate.EliminatedAtRound = currentRoundNumber
			log.Printf("WS PROC_ROUND_END: UserID %d (%s) eliminated.", sessionToUpdate.UserID, sessionToUpdate.User.Username)
		}
		sessionsToUpdate[sessionToUpdate.UserID] = sessionToUpdate

		guessPayload := map[string]float64{"lat": 0, "lng": 0}
		distanceKmPayload := -1.0
		roundScorePayload := 0
		if playerGuessedThisRound {
			guessPayload["lat"] = playerGuessData.GuessLat
			guessPayload["lng"] = playerGuessData.GuessLng
			distanceKmPayload = playerGuessData.DistanceKm
			roundScorePayload = playerGuessData.Score
		}
		playerRoundResultsForPayload = append(playerRoundResultsForPayload, map[string]interface{}{
			"userId": sessionToUpdate.UserID, "username": sessionToUpdate.User.Username, "guess": guessPayload,
			"distanceKm": distanceKmPayload, "roundScore": roundScorePayload, "damageTaken": damageTaken,
			"currentHealth": sessionToUpdate.CurrentHealth, "status": sessionToUpdate.Status, "totalScore": sessionToUpdate.TotalScore,
		})
	}

	errTx := db.Transaction(func(tx *gorm.DB) error {
		for _, sessionPtr := range sessionsToUpdate {
			if err := tx.Save(sessionPtr).Error; err != nil {
				return fmt.Errorf("failed to update session for UserID %d: %w", sessionPtr.UserID, err)
			}
		}
		return nil
	})
	if errTx != nil {
		log.Printf("WS ERROR PROC_ROUND_END: Transaction for health/status updates failed: %v", errTx)
		return
	}

	var actualGameLocationMapping models.MultiplayerGameLocation
	var actualLocationDetails models.Location
	if db.Where("multiplayer_game_id = ? AND round_number = ?", gameDBID, currentRoundNumber).First(&actualGameLocationMapping).Error == nil {
		db.First(&actualLocationDetails, actualGameLocationMapping.LocationID)
	} else {
		log.Printf("WS WARN PROC_ROUND_END: Could not find mapping for actual location for Game %d, Round %d", gameDBID, currentRoundNumber)
	}

	roundEndPayload := gin.H{
		"roundNumber":        currentRoundNumber,
		"actualLocation":     gin.H{"lat": actualLocationDetails.Latitude, "lng": actualLocationDetails.Longitude, "description": actualLocationDetails.Description},
		"playerRoundResults": playerRoundResultsForPayload,
		"currentMultiplier":  damageMultiplier,
	}
	if roundWinnerSession.ID != 0 {
		roundEndPayload["roundWinnerId"] = roundWinnerSession.UserID
	}

	payloadBytes, _ := json.Marshal(roundEndPayload)
	roundEndMsg := WSMessage{Type: MsgTypeRoundEnd, Payload: json.RawMessage(payloadBytes), GameID: gameCode}
	broadcastWSMessage(gameCode, roundEndMsg, nil)
	log.Printf("WS BROADCAST: 'round_end' for Game %s, Round %d", gameCode, currentRoundNumber)

	clearRoundGuesses(gameDBID, currentRoundNumber)

	// IMPORTANT: Do NOT automatically call checkAndProcessGameEndOrNextRound here.
	// Host will trigger it via MsgTypeHostProceedToNextRound.
}

func checkAndProcessGameEndOrNextRound(gameDBID uint, gameCode string, db *gorm.DB) {
	var game models.MultiplayerGame
	if err := db.Preload("PlayerSessions.User").First(&game, gameDBID).Error; err != nil {
		log.Printf("WS ERROR CheckGameEnd: Failed to fetch game %d: %v", gameDBID, err)
		return
	}

	if game.Status == "completed" || game.Status == "aborted" {
		log.Printf("WS CheckGameEnd: Game %d already %s. No action.", gameDBID, game.Status)
		return
	}

	var activePlayersRemaining []models.MultiplayerSession
	for _, s := range game.PlayerSessions {
		if s.IsActive && s.Status == "active" {
			activePlayersRemaining = append(activePlayersRemaining, s)
		}
	}

	gameShouldEnd := false
	reason := ""
	var gameWinner *models.MultiplayerSession = nil

	if len(activePlayersRemaining) <= 1 {
		if game.CurrentRound > 0 || game.Status == "in_progress" {
			gameShouldEnd = true
			if len(activePlayersRemaining) == 1 {
				gameWinner = &activePlayersRemaining[0]
				reason = fmt.Sprintf("Last player standing: %s", gameWinner.User.Username)
			} else {
				reason = "No active players remaining."
			}
		}
	}
	if !gameShouldEnd && game.CurrentRound >= game.RoundsTotal {
		gameShouldEnd = true
		reason = "All rounds completed."
		if len(activePlayersRemaining) > 0 {
			sort.SliceStable(activePlayersRemaining, func(i, j int) bool {
				return activePlayersRemaining[i].TotalScore > activePlayersRemaining[j].TotalScore
			})
			if len(activePlayersRemaining) > 0 {
				gameWinner = &activePlayersRemaining[0]
				if len(activePlayersRemaining) > 1 && activePlayersRemaining[1].TotalScore == gameWinner.TotalScore {
					log.Printf("WS CheckGameEnd: Tie for winner in game %d.", gameDBID)
				}
			}
		}
	}

	if gameShouldEnd {
		log.Printf("WS GAME_END: Game %d (%s) ending. Reason: %s", gameDBID, gameCode, reason)
		if err := db.Model(&models.MultiplayerGame{}).Where("id = ?", gameDBID).Update("status", "completed").Error; err != nil {
			log.Printf("WS ERROR GAME_END: Failed to update DB game status for %d: %v", gameDBID, err)
		}

		var finalSessionsForPayload []models.MultiplayerSession
		db.Preload("User").Where("game_id = ?", gameDBID).
			Order("CASE status WHEN 'active' THEN 0 ELSE 1 END ASC, eliminated_at_round ASC, total_score DESC").
			Find(&finalSessionsForPayload)
		finalStandingsPayload := []map[string]interface{}{}
		for _, s := range finalSessionsForPayload {
			finalStandingsPayload = append(finalStandingsPayload, map[string]interface{}{
				"userId": s.UserID, "username": s.User.Username, "totalScore": s.TotalScore,
				"finalHealth": s.CurrentHealth, "status": s.Status, "eliminatedAtRound": s.EliminatedAtRound,
			})
		}
		gameEndPayload := gin.H{"reason": reason, "finalStandings": finalStandingsPayload}
		if gameWinner != nil {
			gameEndPayload["gameWinnerId"] = gameWinner.UserID
		}

		payloadBytes, _ := json.Marshal(gameEndPayload)
		gameEndMsg := WSMessage{Type: MsgTypeGameEnd, Payload: json.RawMessage(payloadBytes), GameID: gameCode}
		broadcastWSMessage(gameCode, gameEndMsg, nil)
		log.Printf("WS BROADCAST: 'game_end' for Game %s", gameCode)

		activeRoundTimers.Lock()
		if timer, ok := activeRoundTimers.timers[gameDBID]; ok {
			timer.Stop()
			delete(activeRoundTimers.timers, gameDBID)
			log.Printf("WS CLEANUP: Stopped and removed timer for ended game %d.", gameDBID)
		}
		activeRoundTimers.Unlock()
		roundProcessingLocks.Lock()
		for i := 1; i <= game.RoundsTotal; i++ {
			delete(roundProcessingLocks.locks, fmt.Sprintf("%d-%d", gameDBID, i))
		}
		roundProcessingLocks.Unlock()

		gameRooms.Lock()
		delete(gameRooms.rooms, gameCode)
		gameRooms.Unlock()
		clearAllGuessesForGame(gameDBID)
		log.Printf("WS INFO: Game room %s (DBID %d) closed and removed from memory.", gameCode, gameDBID)

	} else { // Start Next Round
		if err := db.Model(&models.MultiplayerGame{}).Where("id = ?", gameDBID).Update("current_round", gorm.Expr("current_round + 1")).Error; err != nil {
			log.Printf("WS ERROR NEXT_ROUND: Failed to increment DB round for game %d: %v", gameDBID, err)
			return
		}
		var updatedGame models.MultiplayerGame
		if err := db.First(&updatedGame, gameDBID).Error; err != nil {
			log.Printf("WS ERROR NEXT_ROUND: Failed to fetch updated game %d: %v", gameDBID, err)
			return
		}
		log.Printf("WS NEXT_ROUND: Game %d (%s) advancing to round %d of %d. Duration: %ds", gameDBID, gameCode, updatedGame.CurrentRound, updatedGame.RoundsTotal, updatedGame.RoundDurationSeconds)

		go db.Model(&models.MultiplayerSession{}).Where("game_id = ? AND is_active = true AND status = 'active'", gameDBID).Update("is_ready", false)
		gameRooms.Lock()
		if room, ok := gameRooms.rooms[gameCode]; ok {
			for _, pInfo := range room {
				var tempSession models.MultiplayerSession
				if db.Where("game_id = ? AND user_id = ? AND is_active = true AND status = 'active'", gameDBID, pInfo.UserID).First(&tempSession).Error == nil {
					pInfo.IsReady = false
				}
			}
		}
		gameRooms.Unlock()
		currentPlayersList := getCurrentPlayersInfoWithDetails(gameCode, gameDBID)
		broadcastDetailedPlayerListUpdate(gameCode, currentPlayersList, nil)

		nextRoundPayload := gin.H{
			"currentRound":         updatedGame.CurrentRound,
			"roundsTotal":          updatedGame.RoundsTotal,
			"roundDurationSeconds": updatedGame.RoundDurationSeconds,
		}

		// Fetch location for the new round with preloading
		currentLoc, _, err := FetchRoundLocations(updatedGame.ID, updatedGame.CurrentRound, true)
		if err != nil {
			log.Printf("WS ERROR NextRound: Failed to fetch location for round %d: %v", updatedGame.CurrentRound, err)
		} else if currentLoc != nil {
			nextRoundPayload["location"] = gin.H{
				"id": currentLoc.ID, "lat": currentLoc.Latitude, "lng": currentLoc.Longitude,
				"description": currentLoc.Description,
			}
		}

		payloadBytes, _ := json.Marshal(nextRoundPayload)
		nextRoundMsg := WSMessage{Type: MsgTypeRoundStart, Payload: json.RawMessage(payloadBytes), GameID: gameCode}
		broadcastWSMessage(gameCode, nextRoundMsg, nil)
		log.Printf("WS BROADCAST: 'round_start' for Game %s, New Round %d", gameCode, updatedGame.CurrentRound)
		// Timer for the new round will be started by the first guess.
	}
}

func handleWSMessage(msg WSMessage, senderInfo *PlayerInfo, gameCode string, gameDBID uint) {
	log.Printf("WS HANDLE: User %d, Type: %s, GameCode: %s, GameDBID: %d", msg.UserID, msg.Type, gameCode, gameDBID)
	db := database.GetDB()

	switch msg.Type {
	case MsgTypePlayerReady:
		var readyData struct {
			IsReady bool `json:"isReady"`
		}
		if err := json.Unmarshal(msg.Payload, &readyData); err != nil {
			log.Printf("WS ERROR PlayerReady: Unmarshal failed for User %d, GameDBID %d: %v", msg.UserID, gameDBID, err)
			sendErrorMessage(senderInfo.Conn, "Invalid ready status payload.")
			return
		}
		err := db.Model(&models.MultiplayerSession{}).Where("game_id = ? AND user_id = ?", gameDBID, msg.UserID).Update("is_ready", readyData.IsReady).Error
		if err != nil {
			log.Printf("WS ERROR PlayerReady: DB update failed for User %d, GameDBID %d: %v", msg.UserID, gameDBID, err)
			sendErrorMessage(senderInfo.Conn, "Failed to update ready status.")
			return
		}
		gameRooms.Lock()
		if player, ok := gameRooms.rooms[gameCode][msg.UserID]; ok {
			player.IsReady = readyData.IsReady
		}
		gameRooms.Unlock()
		currentPlayersList := getCurrentPlayersInfoWithDetails(gameCode, gameDBID)
		broadcastDetailedPlayerListUpdate(gameCode, currentPlayersList, nil)
		log.Printf("WS INFO: User %d in game %s set ready: %t", msg.UserID, gameCode, readyData.IsReady)

	case MsgTypeChatMessage:
		var chatContent struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msg.Payload, &chatContent); err != nil {
			log.Printf("WS ERROR ChatMsg: Unmarshal failed: %v", err)
			return
		}
		enhancedPayload := gin.H{
			"userId": msg.UserID, "username": senderInfo.Username, "content": strings.TrimSpace(chatContent.Content),
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}
		payloadBytes, _ := json.Marshal(enhancedPayload)
		broadcastChatMessage(gameCode, json.RawMessage(payloadBytes), senderInfo)
	case MsgTypeGameStart:
		var gameForStart models.MultiplayerGame
		if err := db.First(&gameForStart, gameDBID).Error; err != nil {
			log.Printf("WS ERROR GameStart: Failed to fetch game %d: %v", gameDBID, err)
			sendErrorMessage(senderInfo.Conn, "Error fetching game details to start.")
			return
		}
		gameRooms.RLock()
		_, roomExists := gameRooms.rooms[gameCode]
		isSenderReallyHost := false
		if pInfo, ok := gameRooms.rooms[gameCode][senderInfo.UserID]; ok {
			isSenderReallyHost = pInfo.IsHost
		}
		gameRooms.RUnlock()

		if !roomExists {
			sendErrorMessage(senderInfo.Conn, "Game lobby not found.")
			return
		}
		if !isSenderReallyHost {
			sendErrorMessage(senderInfo.Conn, "Only the host can start the game.")
			return
		}

		var notReadyCount int64
		db.Model(&models.MultiplayerSession{}).Where("game_id = ? AND is_active = true AND is_ready = false", gameDBID).Count(&notReadyCount)
		if notReadyCount > 0 {
			sendErrorMessage(senderInfo.Conn, "Not all players are ready.")
			return
		}

		var activePlayerCount int64
		db.Model(&models.MultiplayerSession{}).Where("game_id = ? AND is_active = true", gameDBID).Count(&activePlayerCount)
		const minPlayersToStart = 1 // Can be increased
		if activePlayerCount < minPlayersToStart {
			sendErrorMessage(senderInfo.Conn, fmt.Sprintf("Need at least %d players to start.", minPlayersToStart))
			return
		}

		result := db.Model(&models.MultiplayerGame{}).Where("id = ? AND status = ?", gameDBID, "waiting").Updates(map[string]interface{}{"status": "in_progress", "current_round": 1})
		if result.Error != nil {
			log.Printf("WS ERROR GameStart: Failed to update game status for %d: %v", gameDBID, result.Error)
			sendErrorMessage(senderInfo.Conn, "Error starting game on server.")
			return
		}
		if result.RowsAffected == 0 {
			log.Printf("WS WARN GameStart: Game %d was not in 'waiting' status or not found for update.", gameDBID)
			// Re-fetch to ensure client gets current state even if it was already started
			if err := db.First(&gameForStart, gameDBID).Error; err != nil {
				sendErrorMessage(senderInfo.Conn, "Error fetching current game state.")
				return
			}
		} else {
			gameForStart.Status = "in_progress" // Manually update local struct if DB updated
			gameForStart.CurrentRound = 1
		}

		gameStartPayloadData := gin.H{
			"gameDbId": gameForStart.ID, "roundsTotal": gameForStart.RoundsTotal,
			"initialHealth": gameForStart.InitialHealth, "roundDurationSeconds": gameForStart.RoundDurationSeconds}
		gameStartPayloadBytes, _ := json.Marshal(gameStartPayloadData)
		gameStartMsg := WSMessage{Type: MsgTypeGameStart, Payload: json.RawMessage(gameStartPayloadBytes), GameID: gameCode}
		broadcastWSMessage(gameCode, gameStartMsg, nil)
		log.Printf("WS BROADCAST: 'game_start' for Game %s. Rounds: %d, Health: %d, Duration: %ds", gameCode, gameForStart.RoundsTotal, gameForStart.InitialHealth, gameForStart.RoundDurationSeconds)

		firstRoundPayload := gin.H{
			"currentRound": gameForStart.CurrentRound, "roundsTotal": gameForStart.RoundsTotal,
			"roundDurationSeconds": gameForStart.RoundDurationSeconds,
		}

		// Fetch location for the first round with preloading
		currentLoc, _, err := FetchRoundLocations(gameForStart.ID, gameForStart.CurrentRound, true)
		if err != nil {
			log.Printf("WS ERROR GameStart: Failed to fetch location for round %d: %v", gameForStart.CurrentRound, err)
			sendErrorMessage(senderInfo.Conn, "Error fetching round location.")
			return
		}

		if currentLoc != nil {
			firstRoundPayload["location"] = gin.H{
				"id": currentLoc.ID, "lat": currentLoc.Latitude, "lng": currentLoc.Longitude,
				"description": currentLoc.Description,
			}
		}

		roundStartPayloadBytes, _ := json.Marshal(firstRoundPayload)
		firstRoundMsg := WSMessage{Type: MsgTypeRoundStart, Payload: json.RawMessage(roundStartPayloadBytes), GameID: gameCode}
		broadcastWSMessage(gameCode, firstRoundMsg, nil)
		log.Printf("WS BROADCAST: Initial 'round_start' for Game %s, Round %d. Timer will start on first guess.", gameCode, gameForStart.CurrentRound)
		// Timer for the first round is NOT started here anymore. It will start on first guess.

	case MsgTypeGuessSubmit:
		var guessData struct {
			RoundNumber int `json:"roundNumber"`
			Guess       struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"guess"`
		}
		if err := json.Unmarshal(msg.Payload, &guessData); err != nil {
			sendErrorMessage(senderInfo.Conn, "Invalid guess payload.")
			return
		}

		var currentGame models.MultiplayerGame
		var currentSession models.MultiplayerSession
		if err := db.First(&currentGame, gameDBID).Error; err != nil || currentGame.Status != "in_progress" {
			sendErrorMessage(senderInfo.Conn, "Game is not active or not found.")
			return
		}
		if err := db.Where("game_id = ? AND user_id = ?", gameDBID, msg.UserID).First(&currentSession).Error; err != nil || currentSession.Status == "eliminated" || !currentSession.IsActive {
			sendErrorMessage(senderInfo.Conn, "You cannot submit a guess (not active or eliminated).")
			return
		}
		if guessData.RoundNumber != currentGame.CurrentRound {
			sendErrorMessage(senderInfo.Conn, fmt.Sprintf("Guess submitted for wrong round. Current: %d, Submitted: %d", currentGame.CurrentRound, guessData.RoundNumber))
			return
		}

		var existingGuessCount int64
		db.Model(&models.MultiplayerRound{}).Where("session_id = ? AND round_number = ?", currentSession.ID, guessData.RoundNumber).Count(&existingGuessCount)
		if existingGuessCount > 0 {
			sendErrorMessage(senderInfo.Conn, "You have already guessed this round.")
			return
		}

		var gameLocationMapping models.MultiplayerGameLocation
		var actualLocation models.Location
		if err := db.Where("multiplayer_game_id = ? AND round_number = ?", gameDBID, guessData.RoundNumber).First(&gameLocationMapping).Error; err != nil {
			sendErrorMessage(senderInfo.Conn, "Internal error finding round location.")
			return
		}
		if err := db.First(&actualLocation, gameLocationMapping.LocationID).Error; err != nil {
			sendErrorMessage(senderInfo.Conn, "Internal error loading location details.")
			return
		}

		distance := calculateDistance(actualLocation.Latitude, actualLocation.Longitude, guessData.Guess.Lat, guessData.Guess.Lng)
		score := calculateScore(distance)
		var finalPlayerTotalScoreAfterGuess int
		errTx := db.Transaction(func(tx *gorm.DB) error {
			newRound := models.MultiplayerRound{SessionID: currentSession.ID, RoundNumber: guessData.RoundNumber, LocationID: actualLocation.ID, GuessLat: guessData.Guess.Lat, GuessLng: guessData.Guess.Lng, DistanceKm: distance, Score: score}
			if err := tx.Create(&newRound).Error; err != nil {
				return err
			}
			currentSession.TotalScore += score
			if err := tx.Save(&currentSession).Error; err != nil {
				return err
			}
			finalPlayerTotalScoreAfterGuess = currentSession.TotalScore
			return nil
		})
		if errTx != nil {
			sendErrorMessage(senderInfo.Conn, "Error saving your guess.")
			return
		}

		guessSubmitBroadcastPayload := gin.H{"userId": msg.UserID, "username": senderInfo.Username, "roundNumber": guessData.RoundNumber, "score": score, "totalScore": finalPlayerTotalScoreAfterGuess}
		broadcastPayloadBytes, _ := json.Marshal(guessSubmitBroadcastPayload)
		broadcastWSMessage(gameCode, WSMessage{Type: MsgTypeGuessSubmit, Payload: json.RawMessage(broadcastPayloadBytes), GameID: gameCode}, nil)
		log.Printf("WS BROADCAST (Indiv. Guess): User %d for Game %s, Round %d", msg.UserID, gameCode, guessData.RoundNumber)
		recordPlayerGuessForRound(gameDBID, guessData.RoundNumber, msg.UserID)

		activeRoundGuesses.RLock()
		gameRoundGuesses, grExists := activeRoundGuesses.guesses[gameDBID]
		roundGuesses, rExists := gameRoundGuesses[guessData.RoundNumber]
		activeRoundGuesses.RUnlock()

		if grExists && rExists && len(roundGuesses) == 1 { // This is the first guess for this round
			firstGuessPayload := gin.H{"roundNumber": guessData.RoundNumber, "guesserUserId": msg.UserID}
			fgPayloadBytes, _ := json.Marshal(firstGuessPayload)
			broadcastWSMessage(gameCode, WSMessage{Type: MsgTypeFirstPlayerGuessed, Payload: json.RawMessage(fgPayloadBytes), GameID: gameCode}, senderInfo.Conn)
			log.Printf("WS BROADCAST: 'first_player_guessed' by User %d for Game %s, Round %d", msg.UserID, gameCode, guessData.RoundNumber)

			// Start the timer now that the first guess is in
			if currentGame.RoundDurationSeconds > 0 {
				roundKey := fmt.Sprintf("%d-%d", gameDBID, currentGame.CurrentRound)
				roundProcessingLocks.Lock()
				delete(roundProcessingLocks.locks, roundKey) // Ensure no old lock
				roundProcessingLocks.Unlock()

				timer := time.AfterFunc(time.Duration(currentGame.RoundDurationSeconds)*time.Second, func() {
					log.Printf("WS TIMER: Round %d for game %d (%s) expired.", currentGame.CurrentRound, gameDBID, gameCode)
					tryProcessRoundCompletion(gameDBID, gameCode, currentGame.CurrentRound, db)
				})
				activeRoundTimers.Lock()
				if oldTimer, exists := activeRoundTimers.timers[gameDBID]; exists {
					oldTimer.Stop()
				} // Stop any prev timer for this game
				activeRoundTimers.timers[gameDBID] = timer
				activeRoundTimers.Unlock()
				log.Printf("WS TIMER: Started timer (on first guess) for game %d, round %d (%d seconds).", gameDBID, currentGame.CurrentRound, currentGame.RoundDurationSeconds)
			}
		}

		allGuessed, _, errCheck := checkAllActivePlayersGuessed(db, gameDBID, guessData.RoundNumber)
		if errCheck != nil {
			log.Printf("WS ERROR checking if all guessed for game %d round %d: %v", gameDBID, guessData.RoundNumber, errCheck)
		}
		if allGuessed {
			log.Printf("WS INFO: All active players guessed for Game %d, Round %d. Processing completion.", gameDBID, guessData.RoundNumber)
			tryProcessRoundCompletion(gameDBID, gameCode, guessData.RoundNumber, db)
		} else {
			log.Printf("WS INFO: Waiting for more guesses for Game %d, Round %d.", gameDBID, guessData.RoundNumber)
		}

	case MsgTypeHostProceedToNextRound:
		if !senderInfo.IsHost {
			sendErrorMessage(senderInfo.Conn, "Only the host can advance the game.")
			return
		}
		var gameForProceed models.MultiplayerGame
		if err := db.First(&gameForProceed, gameDBID).Error; err != nil {
			log.Printf("WS ERROR HostProceed: Failed to fetch game %d: %v", gameDBID, err)
			sendErrorMessage(senderInfo.Conn, "Error fetching game details.")
			return
		}
		if gameForProceed.Status != "in_progress" {
			sendErrorMessage(senderInfo.Conn, "Game is not currently in progress or has finished.")
			return
		}
		// Additional check: ensure current round has actually ended (e.g., by checking if a round lock exists)
		// This is to prevent advancing if processRoundCompletion hasn't fully run.
		roundKey := fmt.Sprintf("%d-%d", gameDBID, gameForProceed.CurrentRound)
		roundProcessingLocks.RLock()
		_, stillProcessing := roundProcessingLocks.locks[roundKey]
		roundProcessingLocks.RUnlock()

		if stillProcessing {
			log.Printf("WS WARN HostProceed: Round %d for game %d might still be processing. Host requested advance.", gameForProceed.CurrentRound, gameDBID)
			// Optionally send a "please wait" or just let the processing finish.
			// For now, we'll allow it, but it might be better to wait for the lock to clear.
		}

		log.Printf("WS HANDLE: Host User %d proceeding to next round/game end for GameDBID %d", msg.UserID, gameDBID)
		go checkAndProcessGameEndOrNextRound(gameDBID, gameCode, db)

	default:
		log.Printf("WS WARN: Unhandled message type '%s' from User %d (Game %s)", msg.Type, msg.UserID, gameCode)
	}
}
