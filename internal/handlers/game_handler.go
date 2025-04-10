package handlers

import (
	"log"
	"net/http"
	"strconv" // To potentially get round count from query param
	"time"

	"github.com/gin-gonic/gin"
	// Import GORM
	"geoguessr-backend/internal/database" // Adjust import path
	"geoguessr-backend/internal/models"   // Adjust import path
)

const defaultRounds = 5

// Define a struct for the API response to select specific fields
type GameLocationResponse struct {
	ID        uint    `json:"id"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
	// Add Description, Country etc. if needed by frontend later
}

// StartGame fetches random locations for a new game
func StartGame(c *gin.Context) {
	log.Println("Handler: StartGame invoked")

	// Optional: Get number of rounds from query param, default to 5
	roundsStr := c.DefaultQuery("rounds", strconv.Itoa(defaultRounds))
	rounds, err := strconv.Atoi(roundsStr)
	if err != nil || rounds <= 0 {
		rounds = defaultRounds
	}
	log.Printf("Handler: Requesting %d rounds", rounds)

	db := database.GetDB() // Get database connection

	var locations []models.Location
	var responseLocations []GameLocationResponse

	// Query random locations from the database
	// ORDER BY RANDOM() works well for moderately sized tables in PostgreSQL
	// For very large tables, alternative strategies might be needed.
	// We also select specific fields to avoid sending unnecessary data.
	result := db.Model(&models.Location{}).
		Order("RANDOM()"). // PostgreSQL specific for random ordering
		Limit(rounds).
		Find(&locations) // Find fills the locations slice

	if result.Error != nil {
		log.Printf("Error fetching locations: %v", result.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch game locations"})
		return
	}

	if len(locations) < rounds {
		// This shouldn't happen if the DB has enough locations, but good to check
		log.Printf("Warning: Found only %d locations, requested %d", len(locations), rounds)
		// Decide how to handle: error out, or proceed with fewer rounds?
		// For now, let's proceed with what we found if we found at least one.
		if len(locations) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No locations found in database"})
			return
		}
	}

	// Map full model to response struct (selecting fields)
	for _, loc := range locations {
		responseLocations = append(responseLocations, GameLocationResponse{
			ID:        loc.ID, // Send ID for potential future use (e.g., submitting results)
			Latitude:  loc.Latitude,
			Longitude: loc.Longitude,
		})
	}

	log.Printf("Handler: Successfully fetched %d locations", len(responseLocations))
	c.JSON(http.StatusOK, gin.H{
		"gameId":    "mock_backend_game_" + strconv.Itoa(int(time.Now().Unix())), // Placeholder game ID for now
		"locations": responseLocations,
	})
}
