package handlers

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const defaultRounds = 5

type GameLocationResponse struct {
	ID        uint    `json:"id"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

// StartGame godoc
// @Summary      Start a new game
// @Description  Fetches a specified number of random locations for a new game session.
// @Tags         Game
// @Produce      json
// @Param        rounds query int false "Number of rounds (default: 5)" mininum(1) maximum(10)
// @Success      200 {object} map[string]interface{} "Game started successfully (returns gameId placeholder and locations)"
// @Failure      500 {object} map[string]string "Internal server error (failed to fetch locations)"
// @Router       /game/start [get]
func StartGame(c *gin.Context) {
	log.Println("Handler: StartGame invoked")

	roundsStr := c.DefaultQuery("rounds", strconv.Itoa(defaultRounds))
	rounds, err := strconv.Atoi(roundsStr)
	if err != nil || rounds <= 0 {
		rounds = defaultRounds
	}
	log.Printf("Handler: Requesting %d rounds", rounds)

	db := database.GetDB()

	var locations []models.Location
	var responseLocations []GameLocationResponse

	result := db.Model(&models.Location{}).
		Order("RANDOM()").
		Limit(rounds).
		Find(&locations)

	if result.Error != nil {
		log.Printf("Error fetching locations: %v", result.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch game locations"})
		return
	}

	if len(locations) < rounds {

		log.Printf("Warning: Found only %d locations, requested %d", len(locations), rounds)

		if len(locations) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No locations found in database"})
			return
		}
	}

	for _, loc := range locations {
		responseLocations = append(responseLocations, GameLocationResponse{
			ID:        loc.ID,
			Latitude:  loc.Latitude,
			Longitude: loc.Longitude,
		})
	}

	log.Printf("Handler: Successfully fetched %d locations", len(responseLocations))
	c.JSON(http.StatusOK, gin.H{
		"gameId":    "mock_backend_game_" + strconv.Itoa(int(time.Now().Unix())),
		"locations": responseLocations,
	})
}

// FinishGame saves the results of a completed game
// @Summary      Submit game results
// @Description  Saves the total score and round details for a completed game session for the authenticated user.
// @Tags         Game
// @Accept       json
// @Produce      json
// @Param        gameResult body models.SubmitGameInput true "Completed Game Data"
// @Success      201  {object}  map[string]interface{} "Game results saved successfully"
// @Failure      400  {object}  map[string]string "Invalid input format or validation failed"
// @Failure      401  {object}  map[string]string "Unauthorized (invalid/missing token)"
// @Failure      500  {object}  map[string]string "Internal server error (database error)"
// @Security     BearerAuth
// @Router       /game/finish [post]
func FinishGame(c *gin.Context) {
	var input models.SubmitGameInput
	var newGame models.Game // <<< Declare newGame OUTSIDE the transaction scope

	// 1. Get User ID from context
	userIDAny, exists := c.Get("userID")
	if !exists {
		log.Println("FinishGame Error: userID not found in context")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User authentication not found"})
		return
	}
	userID, ok := userIDAny.(uint)
	if !ok {
		log.Println("FinishGame Error: userID in context is not uint")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error processing user identity"})
		return
	}
	log.Printf("FinishGame Handler: Processing submission for user ID: %d", userID)

	// 2. Bind JSON input
	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("FinishGame Error - Binding: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input format: " + err.Error()})
		return
	}

	// 3. Validate input struct
	if err := validate.Struct(input); err != nil {
		log.Printf("FinishGame Error - Validation: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed: " + err.Error()})
		return
	}

	db := database.GetDB()

	// --- Save Game and Rounds within a Transaction ---
	err := db.Transaction(func(tx *gorm.DB) error {
		// 4. Create the main Game record
		// Assign to the newGame declared outside
		newGame = models.Game{
			UserID:       userID,
			TotalScore:   input.TotalScore,
			RoundsPlayed: input.RoundsPlayed,
		}
		if err := tx.Create(&newGame).Error; err != nil {
			log.Printf("FinishGame Error - DB Create Game: %v", err)
			return err
		}
		log.Printf("FinishGame - Created Game record ID: %d", newGame.ID)

		// 5. Create Round records linked to the Game
		for _, roundInput := range input.Rounds {
			newRound := models.Round{
				GameID:      newGame.ID, // Use ID from the created game
				LocationID:  roundInput.LocationID,
				RoundNumber: roundInput.RoundNumber,
				GuessLat:    roundInput.GuessLat,
				GuessLng:    roundInput.GuessLng,
				ActualLat:   roundInput.ActualLat,
				ActualLng:   roundInput.ActualLng,
				DistanceKm:  roundInput.DistanceKm,
				Score:       roundInput.Score,
			}
			if err := tx.Create(&newRound).Error; err != nil {
				log.Printf("FinishGame Error - DB Create Round %d: %v", roundInput.RoundNumber, err)
				return err
			}
		}
		log.Printf("FinishGame - Successfully created %d Round records for Game ID: %d", len(input.Rounds), newGame.ID)
		return nil
	})
	// --- End Transaction ---

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save game results due to database error"})
		return
	}

	// 6. Return Success Response (newGame is now accessible here)
	log.Printf("FinishGame - Successfully saved results for User ID: %d", userID)
	c.JSON(http.StatusCreated, gin.H{
		"message": "Game results saved successfully",
		"gameId":  newGame.ID,
	})
}
