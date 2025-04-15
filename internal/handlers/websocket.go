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
)

var (
	// Configure WebSocket upgrader
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			// For dev purposes - allow any origin
			return true
		},
	}

	// Map to track active game rooms
	gameRooms = struct {
		sync.RWMutex
		rooms map[string]map[*websocket.Conn]bool
	}{
		rooms: make(map[string]map[*websocket.Conn]bool),
	}
)

// Message types for WebSocket communication
const (
	MsgTypePlayerJoin  = "player_join"
	MsgTypePlayerLeave = "player_leave"
	MsgTypeGameStart   = "game_start"
	MsgTypeRoundStart  = "round_start"
	MsgTypeGuessSubmit = "guess_submit"
	MsgTypeRoundEnd    = "round_end"
	MsgTypeGameEnd     = "game_end"
	MsgTypePlayerReady = "player_ready"
	MsgTypeChatMessage = "chat_message"
)

// WSMessage struct for WebSocket communication
type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	GameID  string          `json:"gameId"`
	UserID  uint            `json:"userId"`
}

// HandleWebSocket handles WebSocket connections for multiplayer games
func HandleWebSocket(c *gin.Context) {
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

	// Add connection to game room
	gameRooms.Lock()
	if _, exists := gameRooms.rooms[gameCode]; !exists {
		gameRooms.rooms[gameCode] = make(map[*websocket.Conn]bool)
	}
	gameRooms.rooms[gameCode][conn] = true
	gameRooms.Unlock()

	// Remove connection when function returns
	defer func() {
		gameRooms.Lock()
		delete(gameRooms.rooms[gameCode], conn)
		// If room is empty, remove it
		if len(gameRooms.rooms[gameCode]) == 0 {
			delete(gameRooms.rooms, gameCode)
		}
		gameRooms.Unlock()
	}()

	// Broadcast join to other clients
	broadcastToRoom(gameCode, WSMessage{
		Type:    MsgTypePlayerJoin,
		Payload: json.RawMessage(`{"message": "New player joined"}`),
	}, conn)

	// Message handling loop
	for {
		var msg WSMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("Error reading message: %v", err)
			break
		}

		// Process message based on its type
		handleWSMessage(msg, conn, gameCode)
	}
}

// broadcastToRoom sends a message to all clients in a room except the sender
func broadcastToRoom(gameCode string, msg WSMessage, sender *websocket.Conn) {
	gameRooms.RLock()
	defer gameRooms.RUnlock()

	if clients, exists := gameRooms.rooms[gameCode]; exists {
		for client := range clients {
			if client != sender {
				if err := client.WriteJSON(msg); err != nil {
					log.Printf("Error broadcasting message: %v", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
	}
}

// handleWSMessage processes incoming WebSocket messages
func handleWSMessage(msg WSMessage, conn *websocket.Conn, gameCode string) {
	switch msg.Type {
	case MsgTypePlayerReady:
		// Update player ready status in DB and broadcast to room
		broadcastToRoom(gameCode, msg, nil) // nil = broadcast to all including sender

	case MsgTypeGuessSubmit:
		// Process and save player guess
		// Broadcast guess submission to other players
		broadcastToRoom(gameCode, msg, conn)

	case MsgTypeChatMessage:
		// Simply broadcast chat messages to all clients
		broadcastToRoom(gameCode, msg, nil)

	case MsgTypeGameStart:
		// Update game status and broadcast to all players
		var game models.MultiplayerGame
		if err := database.DB.Where("game_code = ?", gameCode).First(&game).Error; err == nil {
			game.Status = "in_progress"
			game.CurrentRound = 1
			database.DB.Save(&game)
		}
		broadcastToRoom(gameCode, msg, nil)

	case MsgTypePlayerLeave:
		// Update player status in DB and broadcast to others
		broadcastToRoom(gameCode, msg, conn)
	}
}
