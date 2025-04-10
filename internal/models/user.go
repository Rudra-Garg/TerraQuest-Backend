package models

import (
	"time" // Needed for CreatedAt etc. if not using gorm.Model fully

	"gorm.io/gorm"
)

// User represents a registered player
type User struct {
	ID        uint   `gorm:"primaryKey"`
	Username  string `gorm:"size:50;not null;uniqueIndex" validate:"required,min=3,max=50"` // Unique username
	Email     string `gorm:"size:100;not null;uniqueIndex" validate:"required,email"`        // Unique email
	PasswordHash string `gorm:"not null" validate:"required"`                             // Store the hashed password
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"` // Soft delete support

	// Add other profile fields later if needed:
	// GamesPlayed int
	// AverageScore float64
}

// --- Input Structs for Handlers (often placed here or in handlers) ---

// RegisterInput defines the expected input for user registration
type RegisterInput struct {
	Username string `json:"username" validate:"required,min=3,max=50"`
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6,max=72"` // Bcrypt max is 72 bytes
}

// LoginInput defines the expected input for user login
type LoginInput struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}