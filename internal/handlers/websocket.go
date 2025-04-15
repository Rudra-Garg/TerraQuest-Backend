// internal/handlers/websocket.go
package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
	"geoguessr-backend/internal/utils"
)

// PlayerInfo holds data about a connected player
type PlayerInfo struct {
	Conn     *websocket.Conn `json:"-"` // Exclude connection from JSON
	Username string          `json:"username"`
	UserID   uint            `json:"userId"`
	IsHost   bool            `json:"isHost"`
	IsReady  bool            `json:"isReady"`
}

var (
	// Configure WebSocket upgrader
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			// Allow specific origins
			origin := r.Header.Get("Origin")
			allowedOrigins := []string{"http://localhost:5173", "https://rudra-garg.github.io"}
			for _, allowedOrigin := range allowedOrigins {
				if origin == allowedOrigin {
					return true
				}
			}
			return false
		},
	}

	// Map to track active game rooms: gameCode -> userID -> PlayerInfo
	gameRooms = struct {
		sync.RWMutex
		rooms map[string]map[uint]*PlayerInfo
	}{
		rooms: make(map[string]map[uint]*PlayerInfo),
	}
)

// Message types for WebSocket communication
const (
	MsgTypePlayerJoin       = "player_join"
	MsgTypePlayerLeave      = "player_leave"
	MsgTypeGameStart        = "game_start"
	MsgTypeRoundStart       = "round_start"
	MsgTypeGuessSubmit      = "guess_submit"
	MsgTypeRoundEnd         = "round_end"
	MsgTypeGameEnd          = "game_end"
	MsgTypePlayerReady      = "player_ready"
	MsgTypeChatMessage      = "chat_message"
	MsgTypePlayerListUpdate = "player_list_update" // New message type
)

// WSMessage struct for WebSocket communication
type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	GameID  string          `json:"gameId"`
	UserID  uint            `json:"userId"` // UserID of the sender
}

// HandleWebSocket handles WebSocket connections for multiplayer games
func HandleWebSocket(c *gin.Context) {
	// Get token from query param
	token := c.Query("token")
	if token == "" {
		log.Println("No token provided for WebSocket connection")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// Validate token
	parsedToken, err := utils.ValidateToken(token)
	if err != nil {
		log.Printf("Invalid token: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authentication token"})
		return
	}

	// Extract user ID from token
	userId, err := utils.ExtractUserIDFromToken(parsedToken)
	if err != nil {
		log.Printf("Failed to extract user ID: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
		return
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer conn.Close()

	// Get game code from query param
	gameCode := c.Query("gameCode")
	if gameCode == "" {
		log.Println("No game code provided")
		conn.WriteJSON(gin.H{"error": "No game code provided"})
		return
	}

	// Get username from database
	var user models.User
	if err := database.DB.First(&user, userId).Error; err != nil {
		log.Printf("Could not find user with ID %d: %v", userId, err)
		conn.WriteJSON(gin.H{"error": "User not found"})
		return
	}
	username := user.Username

	// Create PlayerInfo
	player := &PlayerInfo{
		Conn:     conn,
		Username: username,
		UserID:   userId,
		IsHost:   false,
		IsReady:  false,
	}

	// Add player to game room
	gameRooms.Lock()
	if _, exists := gameRooms.rooms[gameCode]; !exists {
		gameRooms.rooms[gameCode] = make(map[uint]*PlayerInfo)
		player.IsHost = true // First player is the host
	}
	gameRooms.rooms[gameCode][userId] = player

	// Get current players list BEFORE unlocking
	roomPlayers := make([]*PlayerInfo, 0)
	for _, p := range gameRooms.rooms[gameCode] {
		roomPlayers = append(roomPlayers, &PlayerInfo{
			Username: p.Username,
			UserID:   p.UserID,
			IsHost:   p.IsHost,
			IsReady:  p.IsReady,
		})
	}
	gameRooms.Unlock()

	log.Printf("Player %s (ID: %d) connected to game %s. Host: %t", username, userId, gameCode, player.IsHost)
	log.Printf("Current players in room %s: %+v", gameCode, roomPlayers)

	// First send the current list of players to the newly joined player
	sendPlayerListUpdate(conn, gameCode, roomPlayers)

	// Then broadcast the new player's arrival to everyone else
	broadcastPlayerUpdate(gameCode, player, MsgTypePlayerJoin, conn)

	// Finally, broadcast the updated player list to everyone
	broadcastPlayerListUpdate(gameCode, roomPlayers, nil)

	// Remove player and notify others on disconnect
	defer func() {
		gameRooms.Lock()
		delete(gameRooms.rooms[gameCode], userId)
		// If room is empty, remove it
		if len(gameRooms.rooms[gameCode]) == 0 {
			delete(gameRooms.rooms, gameCode)
			log.Printf("Game room %s closed.", gameCode)
		} else {
			// Notify remaining players
			remainingPlayers := getCurrentPlayers(gameCode)
			gameRooms.Unlock() // Unlock before broadcasting
			broadcastPlayerUpdate(gameCode, player, MsgTypePlayerLeave, nil)
			broadcastPlayerListUpdate(gameCode, remainingPlayers, nil)
		}
		// Ensure unlock happens if not done above
		if gameRooms.TryLock() {
			gameRooms.Unlock()
		}
	}()

	// Message handling loop
	for {
		var msg WSMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Error reading message: %v", err)
			} else {
				log.Printf("Player %s (ID: %d) closed connection for game %s.", username, userId, gameCode)
			}
			break
		}

		log.Printf("Raw message received from %d: %s", userId, msg.Payload)

		// Set the user ID from the authenticated connection
		msg.UserID = userId

		// Process message based on its type
		handleWSMessage(msg, player, gameCode)
	}
}

// getCurrentPlayers safely gets a list of players in a room
func getCurrentPlayers(gameCode string) []*PlayerInfo {
	gameRooms.RLock()
	defer gameRooms.RUnlock()

	room, exists := gameRooms.rooms[gameCode]
	if !exists {
		return []*PlayerInfo{} // Return empty array instead of nil
	}

	players := make([]*PlayerInfo, 0, len(room))
	for _, player := range room {
		// Create a copy without the WebSocket connection
		players = append(players, &PlayerInfo{
			Username: player.Username,
			UserID:   player.UserID,
			IsHost:   player.IsHost,
			IsReady:  player.IsReady,
		})
	}
	return players
}

// sendPlayerListUpdate sends the full player list to a specific connection
func sendPlayerListUpdate(conn *websocket.Conn, gameCode string, players []*PlayerInfo) {
	if players == nil {
		players = []*PlayerInfo{} // Ensure we never send null
	}

	msg := WSMessage{
		Type:    MsgTypePlayerListUpdate,
		Payload: json.RawMessage("[]"), // Initialize with empty array
		GameID:  gameCode,
	}

	// Marshal the players list
	if playersData, err := json.Marshal(players); err == nil {
		msg.Payload = json.RawMessage(playersData)
	} else {
		log.Printf("Error marshaling player list: %v", err)
		return
	}

	if err := conn.WriteJSON(msg); err != nil {
		log.Printf("Error sending player list update: %v", err)
	} else {
		log.Printf("Sent player list update to player: %+v", players)
	}
}

// broadcastPlayerListUpdate sends the full player list to all connections in a room (except sender if specified)
func broadcastPlayerListUpdate(gameCode string, players []*PlayerInfo, sender *websocket.Conn) {
	gameRooms.RLock()
	room, exists := gameRooms.rooms[gameCode]
	if !exists {
		gameRooms.RUnlock()
		return
	}
	gameRooms.RUnlock()

	msg := WSMessage{
		Type:    MsgTypePlayerListUpdate,
		Payload: json.RawMessage("[]"), // Initialize with empty array
	}

	// Marshal the players list
	if playersData, err := json.Marshal(players); err == nil {
		msg.Payload = json.RawMessage(playersData)
	}

	// Broadcast to all players except sender
	for _, player := range room {
		if sender == nil || player.Conn != sender {
			if err := player.Conn.WriteJSON(msg); err != nil {
				log.Printf("Error broadcasting player list update to %d: %v", player.UserID, err)
			}
		}
	}
}

// broadcastPlayerUpdate sends a message about a specific player's action (join/leave/ready)
func broadcastPlayerUpdate(gameCode string, playerInfo *PlayerInfo, messageType string, sender *websocket.Conn) {
	// Create a copy of player info without the WebSocket connection
	playerCopy := &PlayerInfo{
		Username: playerInfo.Username,
		UserID:   playerInfo.UserID,
		IsHost:   playerInfo.IsHost,
		IsReady:  playerInfo.IsReady,
	}

	// Marshal the player info
	payloadBytes, err := json.Marshal(playerCopy)
	if err != nil {
		log.Printf("Error marshaling player info for broadcast (%s): %v", messageType, err)
		return
	}

	msg := WSMessage{
		Type:    messageType,
		Payload: json.RawMessage(payloadBytes),
		GameID:  gameCode,
		UserID:  playerInfo.UserID,
	}

	gameRooms.RLock()
	defer gameRooms.RUnlock()

	if room, exists := gameRooms.rooms[gameCode]; exists {
		log.Printf("Broadcasting %s for player %d to game %s (excluding sender: %t)",
			messageType, playerInfo.UserID, gameCode, sender != nil)

		for _, p := range room {
			if sender == nil || p.Conn != sender {
				log.Printf("Sending %s update about %d to player %d",
					messageType, playerInfo.UserID, p.UserID)
				if err := p.Conn.WriteJSON(msg); err != nil {
					log.Printf("Error broadcasting %s update to %d: %v",
						messageType, p.UserID, err)
				}
			}
		}
	}
}

// handleWSMessage processes incoming WebSocket messages
func handleWSMessage(msg WSMessage, senderInfo *PlayerInfo, gameCode string) {
	gameRooms.Lock() // Use Lock for potential modifications
	room, exists := gameRooms.rooms[gameCode]
	if !exists {
		gameRooms.Unlock()
		log.Printf("Game room %s does not exist for message type %s", gameCode, msg.Type)
		return
	}

	player, playerExists := room[senderInfo.UserID]
	if !playerExists {
		gameRooms.Unlock()
		log.Printf("Player %d not found in game room %s for message type %s", senderInfo.UserID, gameCode, msg.Type)
		return
	}
	gameRooms.Unlock() // Unlock after read, relock if needed for write

	switch msg.Type {
	case MsgTypePlayerJoin:
		// Update player info from the join message
		var joinData struct {
			Username string `json:"username"`
			UserID   uint   `json:"userId"`
			IsHost   bool   `json:"isHost"`
		}
		if err := json.Unmarshal(msg.Payload, &joinData); err != nil {
			log.Printf("Error parsing join payload from %d: %v", senderInfo.UserID, err)
			return
		}

		gameRooms.Lock()
		player.Username = joinData.Username
		player.IsHost = joinData.IsHost
		log.Printf("Player %s (ID: %d) joined game %s. Host: %t", player.Username, player.UserID, gameCode, player.IsHost)
		gameRooms.Unlock()

		// Send updated player list to all players
		players := getCurrentPlayers(gameCode)
		broadcastPlayerListUpdate(gameCode, players, nil)

		// Also send a player_join message to all other players
		broadcastPlayerUpdate(gameCode, player, MsgTypePlayerJoin, senderInfo.Conn)

	case MsgTypePlayerReady:
		var readyData struct {
			IsReady bool `json:"isReady"`
		}
		if err := json.Unmarshal(msg.Payload, &readyData); err != nil {
			log.Printf("Error parsing ready status payload from %d: %v", senderInfo.UserID, err)
			return
		}

		gameRooms.Lock()
		player.IsReady = readyData.IsReady
		log.Printf("Player %s (ID: %d) readiness set to %t in game %s", player.Username, player.UserID, player.IsReady, gameCode)
		gameRooms.Unlock()

		// Send updated player list to all players
		players := getCurrentPlayers(gameCode)
		broadcastPlayerListUpdate(gameCode, players, nil)

	case MsgTypeGuessSubmit:
		// Process and save player guess (TODO)
		// Broadcast guess submission to other players
		broadcastPlayerUpdate(gameCode, player, MsgTypeGuessSubmit, senderInfo.Conn) // Send full player info or just guess?

	case MsgTypeChatMessage:
		// Simple broadcast of chat payload
		broadcastGenericMessage(gameCode, msg, senderInfo.Conn)

	case MsgTypeGameStart:
		// Only host can start
		if !player.IsHost {
			log.Printf("Non-host player %d attempted to start game %s", player.UserID, gameCode)
			// TODO: Send error message back to player
			return
		}

		// Get game settings (needed for rounds, etc.)
		var game models.MultiplayerGame
		if err := database.DB.Where("game_code = ?", gameCode).First(&game).Error; err != nil {
			log.Printf("Error fetching game %s for start: %v", gameCode, err)
			// TODO: Send error message back to host
			return
		}

		// Check if all players are ready
		gameRooms.RLock()
		allReady := true
		if len(room) < 2 { // Require at least 2 players (Adjust as needed)
			allReady = false
		} else {
			for _, p := range room {
				if !p.IsReady {
					allReady = false
					break
				}
			}
		}
		gameRooms.RUnlock()

		if !allReady {
			log.Printf("Host %d attempted to start game %s before all players were ready", player.UserID, gameCode)
			// TODO: Send error message back to host
			return
		}

		// Fetch locations using GORM query directly
		db := database.GetDB() // Get DB instance
		var locations []models.Location
		result := db.Model(&models.Location{}).
			Order("RANDOM()").
			Limit(int(game.RoundsTotal)).
			Find(&locations)

		if result.Error != nil {
			log.Printf("Error getting locations for game %s: %v", gameCode, result.Error)
			// TODO: Send error message back to host
			return
		}
		if result.RowsAffected < int64(game.RoundsTotal) {
			log.Printf("Warning: Found only %d locations for game %s, requested %d", result.RowsAffected, gameCode, game.RoundsTotal)
			if result.RowsAffected == 0 {
				// TODO: Send error message back to host
				return
			}
		}

		// Update game status in DB
		game.Status = "in_progress"
		game.CurrentRound = 1
		if err := database.DB.Save(&game).Error; err != nil {
			log.Printf("Error updating game status for %s: %v", gameCode, err)
			// TODO: Send error message back to host
			return
		}

		// Prepare payload with locations
		startPayload := struct {
			Locations []models.Location `json:"locations"`
		}{
			Locations: locations,
		}
		payloadBytes, err := json.Marshal(startPayload)
		if err != nil {
			log.Printf("Error marshaling start game payload: %v", err)
			return
		}
		msg.Payload = json.RawMessage(payloadBytes)

		// Broadcast game start to all players (including host)
		log.Printf("Host %d starting game %s with %d locations", player.UserID, gameCode, len(locations))
		broadcastGenericMessage(gameCode, msg, nil) // Send to all

	// Note: MsgTypePlayerLeave is handled by the defer function in HandleWebSocket
	// Note: MsgTypePlayerJoin is handled by the connection logic in HandleWebSocket

	default:
		log.Printf("Received unknown message type '%s' from player %d", msg.Type, senderInfo.UserID)
	}
}

// broadcastGenericMessage sends a generic WSMessage to all clients in a room except the sender
func broadcastGenericMessage(gameCode string, msg WSMessage, sender *websocket.Conn) {
	gameRooms.RLock()
	defer gameRooms.RUnlock()

	if room, exists := gameRooms.rooms[gameCode]; exists {
		for _, player := range room {
			if player.Conn != sender {
				if err := player.Conn.WriteJSON(msg); err != nil {
					log.Printf("Error broadcasting generic message type %s to %d: %v", msg.Type, player.UserID, err)
					// Optionally handle connection closure here
				}
			}
		}
	}
}

// TODO: Add function to update player status in DB when ready status changes.
// TODO: Add function to handle Game Start logic (generating locations, etc.)
// TODO: Add function to handle Guess Submission logic.
// TODO: Properly determine host status when joining/creating.
