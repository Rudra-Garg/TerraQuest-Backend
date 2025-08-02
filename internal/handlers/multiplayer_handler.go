// internal/handlers/multiplayer_handler.go
package handlers

import (
	"fmt"
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
	"log"
	"math/rand"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const gameCodeLength = 6
const gameCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // Omitting I, O, 0, 1

// Default values for game creation if not provided or invalid from client
const defaultInitialHealth = 6000      // Default health
const defaultRoundDurationSeconds = 60 // Default round duration in seconds

// Generates a random game code
func generateGameCode() string {
	code := make([]byte, gameCodeLength)
	for i := range code {
		code[i] = gameCodeChars[rand.Intn(len(gameCodeChars))]
	}
	return string(code)
}

type MultiplayerGameLocation struct {
	MultiplayerGameID uint `gorm:"primaryKey"`
	LocationID        uint `gorm:"primaryKey"`
	RoundNumber       int  `gorm:"not null"` // Ensure this matches the DB constraint
}

func (MultiplayerGameLocation) TableName() string {
	return "multiplayer_game_locations"
}

// CreateMultiplayerGame godoc
// @Summary      Create Multiplayer Game
// @Description  Creates a new waiting lobby for a multiplayer game.
// @Tags         Multiplayer
// @Accept       json
// @Produce      json
// @Param        gameOptions body models.CreateGameInput true "Game Options (Max Players, Rounds, Initial Health, Round Duration)"
// @Success      200  {object}  map[string]interface{} "Game created successfully (returns gameCode and gameId)"
// @Failure      400  {object}  map[string]string "Invalid input"
// @Failure      401  {object}  map[string]string "Unauthorized"
// @Failure      500  {object}  map[string]string "Internal server error"
// @Security     BearerAuth
// @Router       /multiplayer/create [post]
func CreateMultiplayerGame(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in token"})
		return
	}
	hostUserID := userID.(uint)

	var input models.CreateGameInput
	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("CreateMPGame Error - Binding: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input: " + err.Error()})
		return
	}

	// Apply defaults if values are not provided or are out of typical range
	// (although `binding` tags in the model handle min/max validation for request body)
	if input.InitialHealth <= 0 {
		input.InitialHealth = defaultInitialHealth
	}
	// <<< NEW: Apply default for RoundDurationSeconds
	if input.RoundDurationSeconds <= 0 {
		input.RoundDurationSeconds = defaultRoundDurationSeconds
	}
	// <<< END NEW
	db := database.GetDB()
	var gameCode string
	var game models.MultiplayerGame

	// --- Transaction for Game + Session Creation ---
	err := db.Transaction(func(tx *gorm.DB) error {
		// 1. Generate unique game code
		for i := 0; i < 5; i++ {
			gameCode = generateGameCode()
			var count int64
			if tx.Model(&models.MultiplayerGame{}).Where("game_code = ?", gameCode).Count(&count); count == 0 {
				break
			}
			if i == 4 {
				return fmt.Errorf("failed to generate unique game code")
			}
		}
		// 2. Create the game record (without pre-fetching all locations)
		game = models.MultiplayerGame{
			HostUserID:           hostUserID,
			GameCode:             gameCode,
			Status:               "waiting",
			MaxPlayers:           input.MaxPlayers,
			RoundsTotal:          input.RoundsTotal,
			InitialHealth:        input.InitialHealth,
			RoundDurationSeconds: input.RoundDurationSeconds,
			CurrentRound:         0,
		}
		if err := tx.Create(&game).Error; err != nil {
			return fmt.Errorf("failed to create game record: %w", err)
		}
		log.Printf("CreateMPGame: Created Game record DBID %d with InitialHealth %d and RoundDuration %d (per-round location loading enabled)", game.ID, game.InitialHealth, game.RoundDurationSeconds)

		// 5. Create the host's session record
		hostSession := models.MultiplayerSession{
			GameID:        game.ID,
			UserID:        hostUserID,
			IsHost:        true,
			IsReady:       false,
			IsActive:      true,
			CurrentHealth: game.InitialHealth, // Host also gets initial health
			Status:        "active",
		}
		if err := tx.Create(&hostSession).Error; err != nil {
			return fmt.Errorf("failed to create host session: %w", err)
		}

		return nil
	})
	// --- End Transaction ---

	if err != nil {
		log.Printf("CreateMPGame Error - Transaction failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create multiplayer game: " + err.Error()})
		return
	}

	log.Printf("Multiplayer game created: Code=%s, DBID=%d, Host=%d, Rounds=%d, InitialHealth=%d, RoundDuration=%ds", game.GameCode, game.ID, game.HostUserID, game.RoundsTotal, game.InitialHealth, game.RoundDurationSeconds) // <<< MODIFIED Log
	c.JSON(http.StatusOK, gin.H{
		"message":  "Game created successfully",
		"gameCode": game.GameCode,
		"gameId":   game.ID,
	})
}

// JoinMultiplayerGame godoc
// @Summary      Join Multiplayer Game
// @Description  Allows an authenticated user to join an existing waiting game lobby using a game code.
// @Tags         Multiplayer
// @Produce      json
// @Param        gameCode path string true "The 6-character game code"
// @Success      200  {object}  map[string]interface{} "Successfully joined game (returns gameId)"
// @Failure      400  {object}  map[string]string "Game is full or already started"
// @Failure      401  {object}  map[string]string "Unauthorized"
// @Failure      404  {object}  map[string]string "Game not found"
// @Failure      500  {object}  map[string]string "Internal server error"
// @Security     BearerAuth
// @Router       /multiplayer/join/{gameCode} [post]
func JoinMultiplayerGame(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in token"})
		return
	}
	joinUserID := userID.(uint)
	gameCodeParam := strings.ToUpper(c.Param("gameCode"))

	db := database.GetDB()
	var game models.MultiplayerGame

	result := db.Where("game_code = ? AND status = ?", gameCodeParam, "waiting").First(&game)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			log.Printf("JoinMPGame: Game code %s not found or not waiting.", gameCodeParam)
			c.JSON(http.StatusNotFound, gin.H{"error": "Game not found or has already started."})
		} else {
			log.Printf("JoinMPGame Error - Finding game %s: %v", gameCodeParam, result.Error)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error finding game"})
		}
		return
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var currentSessions []models.MultiplayerSession
		if err := tx.Where("game_id = ?", game.ID).Find(&currentSessions).Error; err != nil {
			return fmt.Errorf("failed to count players: %w", err)
		}

		playerAlreadyInSession := false
		var existingSession models.MultiplayerSession
		activePlayerCount := 0

		for _, s := range currentSessions {
			if s.UserID == joinUserID {
				existingSession = s
				playerAlreadyInSession = true
			}
			if s.IsActive {
				activePlayerCount++
			}
		}

		if playerAlreadyInSession {
			if !existingSession.IsActive {
				existingSession.IsActive = true
				existingSession.LeftAt = nil
				if err := tx.Save(&existingSession).Error; err != nil {
					return fmt.Errorf("failed to reactivate existing session: %w", err)
				}
				log.Printf("JoinMPGame: Reactivated player %d in game %d. Health: %d", joinUserID, game.ID, existingSession.CurrentHealth)
				activePlayerCount++
			} else {
				log.Printf("JoinMPGame: Player %d already active in game %d", joinUserID, game.ID)
			}
		} else {
			if activePlayerCount >= game.MaxPlayers {
				return fmt.Errorf("game is full")
			}
			newSession := models.MultiplayerSession{
				GameID:        game.ID,
				UserID:        joinUserID,
				IsHost:        false,
				IsReady:       false,
				IsActive:      true,
				CurrentHealth: game.InitialHealth, // New player gets game's initial health
				Status:        "active",
			}
			if err := tx.Create(&newSession).Error; err != nil {
				return fmt.Errorf("failed to create new player session: %w", err)
			}
			log.Printf("JoinMPGame: Player %d successfully joined game %d with health %d", joinUserID, game.ID, newSession.CurrentHealth)
		}
		return nil
	})

	if err != nil {
		log.Printf("JoinMPGame Error - Transaction failed for game %s: %v", gameCodeParam, err)
		if err.Error() == "game is full" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Game is full"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to join game: " + err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Successfully joined game",
		"gameId":  game.ID,
	})
}

// GetMultiplayerGameState (Skeleton for Phase 1 - can be expanded later)
// @Summary      Get Multiplayer Game State (Basic)
// @Description  Retrieves basic info about a game lobby (like game code, host). Player list will be via WebSocket.
// @Tags         Multiplayer
// @Produce      json
// @Param        gameId path uint true "The Database ID of the game"
// @Success      200  {object}  map[string]interface{} "Basic game info"
// @Failure      401  {object}  map[string]string "Unauthorized"
// @Failure      403  {object}  map[string]string "Forbidden (not part of game)"
// @Failure      404  {object}  map[string]string "Game not found"
// @Failure      500  {object}  map[string]string "Internal server error"
// @Security     BearerAuth
// @Router       /multiplayer/game/{gameId} [get]
func GetMultiplayerGameState(c *gin.Context) {
	userID, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in token"})
		return
	}
	requestUserID := userID.(uint)
	gameID := c.Param("gameId")

	db := database.GetDB()
	var game models.MultiplayerGame
	if err := db.Preload("HostUser").First(&game, gameID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Game not found"})
		} else {
			log.Printf("GetMPGameState Error - Finding game %s: %v", gameID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error finding game"})
		}
		return
	}

	var sessionCount int64
	db.Model(&models.MultiplayerSession{}).Where("game_id = ? AND user_id = ? AND is_active = ?", game.ID, requestUserID, true).Count(&sessionCount)
	if sessionCount == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not an active participant in this game"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"gameId":        game.ID,
		"gameCode":      game.GameCode,
		"status":        game.Status,
		"hostUsername":  game.HostUser.Username,
		"maxPlayers":    game.MaxPlayers,
		"roundsTotal":   game.RoundsTotal,
		"initialHealth": game.InitialHealth, // <<< MODIFIED: Include initial health		"roundDurationSeconds": game.RoundDurationSeconds, // <<< MODIFIED: Include round duration
	})
}

// FetchRoundLocations fetches location(s) for specific round(s) and stores them in the join table
// This supports per-round location loading with optional preloading of the next round
func FetchRoundLocations(gameID uint, roundNumber int, preloadNext bool) (currentLoc *models.Location, nextLoc *models.Location, err error) {
	db := database.GetDB()
	// Check if location for current round already exists
	var existingMapping models.MultiplayerGameLocation
	if err := db.Where("multiplayer_game_id = ? AND round_number = ?", gameID, roundNumber).First(&existingMapping).Error; err == nil {
		// Location already exists, fetch it
		var currentLocation models.Location
		if err := db.First(&currentLocation, existingMapping.LocationID).Error; err != nil {
			return nil, nil, fmt.Errorf("failed to fetch existing location for round %d: %w", roundNumber, err)
		}
		currentLoc = &currentLocation
		log.Printf("FetchRoundLocations: Using existing location for Game %d, Round %d", gameID, roundNumber)
	} else {
		// Fetch a new random location for current round that avoids duplicates
		var usedLocationIDs []uint
		db.Model(&models.MultiplayerGameLocation{}).
			Where("multiplayer_game_id = ?", gameID).
			Pluck("location_id", &usedLocationIDs)

		query := db.Model(&models.Location{}).Order("RANDOM()")
		if len(usedLocationIDs) > 0 {
			query = query.Where("id NOT IN ?", usedLocationIDs)
		}

		var selectedLocations []models.Location
		if err := query.Limit(1).Find(&selectedLocations).Error; err != nil {
			return nil, nil, fmt.Errorf("failed to fetch random location for round %d: %w", roundNumber, err)
		}
		if len(selectedLocations) == 0 {
			return nil, nil, fmt.Errorf("no locations available for round %d", roundNumber)
		}

		currentLoc = &selectedLocations[0]

		// Store in join table
		joinRecord := models.MultiplayerGameLocation{
			MultiplayerGameID: gameID,
			LocationID:        currentLoc.ID,
			RoundNumber:       roundNumber,
		}
		if err := db.Create(&joinRecord).Error; err != nil {
			return nil, nil, fmt.Errorf("failed to store location mapping for round %d: %w", roundNumber, err)
		}
		log.Printf("FetchRoundLocations: Fetched and stored new location for Game %d, Round %d", gameID, roundNumber)
	}

	// Preload next round location if requested
	if preloadNext {
		nextRoundNumber := roundNumber + 1
		var nextMapping models.MultiplayerGameLocation
		if err := db.Where("multiplayer_game_id = ? AND round_number = ?", gameID, nextRoundNumber).First(&nextMapping).Error; err == nil {
			// Next round location already exists, fetch it
			var nextLocation models.Location
			if err := db.First(&nextLocation, nextMapping.LocationID).Error; err == nil {
				nextLoc = &nextLocation
				log.Printf("FetchRoundLocations: Found existing preloaded location for Game %d, Round %d", gameID, nextRoundNumber)
			}
		} else {
			// Fetch and store next round location, avoiding duplicates
			var nextUsedLocationIDs []uint
			db.Model(&models.MultiplayerGameLocation{}).
				Where("multiplayer_game_id = ?", gameID).
				Pluck("location_id", &nextUsedLocationIDs)

			nextQuery := db.Model(&models.Location{}).Order("RANDOM()")
			if len(nextUsedLocationIDs) > 0 {
				nextQuery = nextQuery.Where("id NOT IN ?", nextUsedLocationIDs)
			}

			var nextSelectedLocations []models.Location
			if err := nextQuery.Limit(1).Find(&nextSelectedLocations).Error; err == nil && len(nextSelectedLocations) > 0 {
				nextLoc = &nextSelectedLocations[0]

				// Store in join table
				nextJoinRecord := models.MultiplayerGameLocation{
					MultiplayerGameID: gameID,
					LocationID:        nextLoc.ID,
					RoundNumber:       nextRoundNumber,
				}
				if err := db.Create(&nextJoinRecord).Error; err == nil {
					log.Printf("FetchRoundLocations: Preloaded location for Game %d, Round %d", gameID, nextRoundNumber)
				}
			}
		}
	}

	return currentLoc, nextLoc, nil
}
