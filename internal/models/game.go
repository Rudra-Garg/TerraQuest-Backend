// internal/models/game.go
package models

import (
	"time"
)

// Game represents a completed game session by a user
type Game struct {
	ID           uint      `gorm:"primaryKey"`
	UserID       uint      `gorm:"not null;index"` // Foreign key to User table
	User         User      // GORM association (belongs to User) - optional but useful for preloading
	TotalScore   int       `gorm:"not null"`
	RoundsPlayed int       `gorm:"not null"` // Typically 5, but could be variable
	CreatedAt    time.Time // When the game was completed/saved
	UpdatedAt    time.Time

	Rounds []Round `gorm:"foreignKey:GameID"` // GORM association (has many Rounds)
}

// RoundResultInput represents the data for a single round sent from frontend
type RoundResultInput struct {
	RoundNumber int     `json:"roundNumber" validate:"required,min=1"`
	LocationID  uint    `json:"locationId" validate:"required,min=1"`
	GuessLat    float64 `json:"guessLat" validate:"required"`
	GuessLng    float64 `json:"guessLng" validate:"required"`
	ActualLat   float64 `json:"actualLat" validate:"required"` // Send actual coords for verification/record
	ActualLng   float64 `json:"actualLng" validate:"required"`
	DistanceKm  float64 `json:"distanceKm" validate:"min=0"`
	Score       int     `json:"score" validate:"required,min=0"`
}

// SubmitGameInput represents the complete game data sent from frontend
type SubmitGameInput struct {
	// GameID      string             `json:"gameId"` // We might generate ID on backend instead
	TotalScore   int                `json:"totalScore" validate:"required,min=0"`
	RoundsPlayed int                `json:"roundsPlayed" validate:"required,min=1"`
	Rounds       []RoundResultInput `json:"rounds" validate:"required,min=1,dive"` // 'dive' validates each element in slice
}
