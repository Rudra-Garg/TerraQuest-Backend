// internal/handlers/multiplayer_handler.go
package handlers

import (
	"math/rand"
	"net/http"
	"time"
	"strings"

	"github.com/gin-gonic/gin"
	
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
)

const gameCodeLength = 6
const gameCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // Omitting easily confused characters

// Generates a random game code for multiplayer games
func generateGameCode() string {
	rand.Seed(time.Now().UnixNano())
	code := make([]byte, gameCodeLength)
	for i := range code {
		code[i] = gameCodeChars[rand.Intn(len(gameCodeChars))]
	}
	return string(code)
}

// CreateMultiplayerGame creates a new multiplayer game
func CreateMultiplayerGame(c *gin.Context) {
	userID := c.GetUint("userID") // From auth middleware
	
	var input struct {
		MaxPlayers  int `json:"maxPlayers" binding:"required,min=2,max=8"`
		RoundsTotal int `json:"roundsTotal" binding:"required,min=1,max=10"`
	}
	
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	
	// Generate unique game code
	gameCode := generateGameCode()
	
	// Check if code already exists and regenerate if needed
	for i := 0; i < 5; i++ { // Try up to 5 times
		var existingGame models.MultiplayerGame
		if database.DB.Where("game_code = ?", gameCode).First(&existingGame).Error != nil {
			// No game found with this code, it's unique
			break
		}
		// Code exists, generate a new one
		gameCode = generateGameCode()
	}
	
	// Get random locations for the game
	var locations []models.Location
	if err := database.DB.Order("RANDOM()").Limit(input.RoundsTotal).Find(&locations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get game locations"})
		return
	}
	
	if len(locations) < input.RoundsTotal {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Not enough locations available"})
		return
	}
	
	// Create multiplayer game in DB
	game := models.MultiplayerGame{
		HostUserID:   userID,
		GameCode:     gameCode,
		Status:       "waiting",
		MaxPlayers:   input.MaxPlayers,
		RoundsTotal:  input.RoundsTotal,
		CurrentRound: 0,
		Locations:    locations,
	}
	
	// Save game to database
	if err := database.DB.Create(&game).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create game"})
		return
	}
	
	// Create host session
	session := models.MultiplayerSession{
		GameID:    game.ID,
		UserID:    userID,
		IsHost:    true,
		IsReady:   false,
		IsActive:  true,
		JoinedAt:  time.Now(),
	}
	
	if err := database.DB.Create(&session).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create host session"})
		return
	}
	
	c.JSON(http.StatusOK, gin.H{
		"gameCode": gameCode,
		"gameId":   game.ID,
	})
}

// JoinMultiplayerGame allows a player to join an existing game
func JoinMultiplayerGame(c *gin.Context) {
	userID := c.GetUint("userID")
	gameCode := c.Param("gameCode")
	
	// Normalize game code (uppercase)
	gameCode = strings.ToUpper(gameCode)
	
	var game models.MultiplayerGame
	if err := database.DB.Where("game_code = ? AND status = 'waiting'", gameCode).First(&game).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found or already started"})
		return
	}
	
	// Check if game is full
	var count int64
	database.DB.Model(&models.MultiplayerSession{}).Where("game_id = ? AND is_active = true", game.ID).Count(&count)
	if int(count) >= game.MaxPlayers {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Game is full"})
		return
	}
	
	// Check if player already joined
	var existingSession models.MultiplayerSession
	result := database.DB.Where("game_id = ? AND user_id = ?", game.ID, userID).First(&existingSession)
	if result.Error == nil {
		// Player already joined, update session
		existingSession.IsActive = true
		database.DB.Save(&existingSession)
		c.JSON(http.StatusOK, gin.H{"gameId": game.ID})
		return
	}
	
	// Create new session
	session := models.MultiplayerSession{
		GameID:    game.ID,
		UserID:    userID,
		IsHost:    false,
		IsReady:   false,
		IsActive:  true,
		JoinedAt:  time.Now(),
	}
	
	if err := database.DB.Create(&session).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to join game"})
		return
	}
	
	c.JSON(http.StatusOK, gin.H{"gameId": game.ID})
}

// GetMultiplayerGameState returns the current state of a multiplayer game
func GetMultiplayerGameState(c *gin.Context) {
	gameID := c.Param("gameId")
	userID := c.GetUint("userID")
	
	var game models.MultiplayerGame
	if err := database.DB.Preload("PlayerSessions.User").Preload("Locations").First(&game, gameID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		return
	}
	
	// Check if user is a participant
	var isParticipant bool
	for _, session := range game.PlayerSessions {
		if session.UserID == userID && session.IsActive {
			isParticipant = true
			break
		}
	}
	
	if !isParticipant {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not a participant in this game"})
		return
	}
	
	// Prepare player data (exclude sensitive info)
	type PlayerInfo struct {
		UserID    uint   `json:"userId"`
		Username  string `json:"username"`
		IsHost    bool   `json:"isHost"`
		IsReady   bool   `json:"isReady"`
		TotalScore int   `json:"totalScore"`
	}
	
	players := make([]PlayerInfo, 0)
	for _, session := range game.PlayerSessions {
		if session.IsActive {
			players = append(players, PlayerInfo{
				UserID:     session.UserID,
				Username:   session.User.Username,
				IsHost:     session.IsHost,
				IsReady:    session.IsReady,
				TotalScore: session.TotalScore,
			})
		}
	}
	
	// Only send location data if game is in progress
	var locationsData []gin.H
	if game.Status == "in_progress" || game.Status == "completed" {
		locationsData = make([]gin.H, len(game.Locations))
		for i, loc := range game.Locations {
			locationsData[i] = gin.H{
				"id":  loc.ID,
				"lat": loc.Latitude,
				"lng": loc.Longitude,
			}
		}
	}
	
	c.JSON(http.StatusOK, gin.H{
		"gameId":       game.ID,
		"gameCode":     game.GameCode,
		"status":       game.Status,
		"currentRound": game.CurrentRound,
		"roundsTotal":  game.RoundsTotal,
		"players":      players,
		"locations":    locationsData,
	})
} 