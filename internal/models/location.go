package models

import "gorm.io/gorm"

// Location represents a potential spot in the game
type Location struct {
	gorm.Model        // Includes fields like ID, CreatedAt, UpdatedAt, DeletedAt
	Latitude   float64 `gorm:"not null"`
	Longitude  float64 `gorm:"not null"`
	Description string  // Optional description (e.g., "Near Eiffel Tower")
	Country    string  // Optional country info
	Region     string  // Optional region/state info
	// Add other fields if desired (e.g., Difficulty, ImageURL)
}

// You can add methods to the Location struct later if needed
// func (l *Location) TableName() string {
//  return "locations" // GORM usually infers this, but you can override
// }