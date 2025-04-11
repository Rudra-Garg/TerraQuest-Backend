// internal/models/round.go
package models

import (
	"time"

	"gorm.io/gorm"
)

// Round represents the result of a single round within a game
type Round struct {
	ID          uint     `gorm:"primaryKey"`
	GameID      uint     `gorm:"not null;index"` // Foreign key to Game table
	LocationID  uint     `gorm:"not null"`       // Foreign key to Location table
	Location    Location // GORM association (belongs to Location) - optional
	RoundNumber int      `gorm:"not null"` // e.g., 1, 2, 3, 4, 5
	GuessLat    float64  `gorm:"not null"` // User's guessed latitude
	GuessLng    float64  `gorm:"not null"` // User's guessed longitude
	ActualLat   float64  `gorm:"not null"` // Actual latitude (from Location)
	ActualLng   float64  `gorm:"not null"` // Actual longitude (from Location)
	DistanceKm  float64  // Calculated distance in km
	Score       int      `gorm:"not null"` // Score for this round
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"` // Soft delete support
}
