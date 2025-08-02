// internal/models/multiplayer.go
package models

import (
	"time"
)

type MultiplayerGame struct {
	ID            uint      `gorm:"primaryKey"`
	HostUserID    uint      `gorm:"not null;index"`                              // User who created the game
	GameCode      string    `gorm:"type:varchar(8);uniqueIndex;not null"`        // Short code for joining
	Status        string    `gorm:"type:varchar(20);not null;default:'waiting'"` // "waiting", "in_progress", "completed", "aborted"
	MaxPlayers    int       `gorm:"not null;default:4"`
	RoundsTotal   int       `gorm:"not null;default:5"`
	CurrentRound  int       `gorm:"not null;default:0"`   // 0 = Lobby/Not Started
	InitialHealth int       `gorm:"not null;default:6000"`
	RoundDurationSeconds int `gorm:"not null;default:30"` // Duration of each round in seconds
	CreatedAt     time.Time `gorm:"autoCreateTime"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime"`

	HostUser       User                 `gorm:"foreignKey:HostUserID"` // Belongs To relationship
	PlayerSessions []MultiplayerSession `gorm:"foreignKey:GameID"`     // Has Many relationship
	Locations      []Location           `gorm:"many2many:multiplayer_game_locations;"`
}

// MultiplayerSession represents a player's participation in a specific multiplayer game
type MultiplayerSession struct {
	ID            uint       `gorm:"primaryKey"`
	GameID        uint       `gorm:"not null;index"` // Foreign key to MultiplayerGame
	UserID        uint       `gorm:"not null;index"` // Foreign key to User
	IsHost        bool       `gorm:"not null;default:false"`
	IsReady       bool       `gorm:"not null;default:false"`
	TotalScore    int        `gorm:"not null;default:0"`
	IsActive      bool       `gorm:"not null;default:true"`                      // Track if player is currently connected/in game
	CurrentHealth int        `gorm:"not null;default:100"`                       // <<< NEW: Player's current health
	Status        string     `gorm:"type:varchar(20);not null;default:'active'"` // <<< NEW: "active", "eliminated"
	EliminatedAtRound int        `gorm:"default:0"`
	JoinedAt      time.Time  `gorm:"autoCreateTime"`
	LeftAt        *time.Time // Pointer to allow NULL

	User   User               `gorm:"foreignKey:UserID"` // Belongs To relationship
	Game   MultiplayerGame    `gorm:"foreignKey:GameID"` // Belongs To relationship
	Rounds []MultiplayerRound `gorm:"foreignKey:SessionID"`
}

// CreateGameInput represents options for creating a new multiplayer game
type CreateGameInput struct {
	MaxPlayers    int `json:"maxPlayers" binding:"required,min=1,max=8"` // Min 1 for testing, usually 2
	RoundsTotal   int `json:"roundsTotal" binding:"required,min=1,max=10"`
	InitialHealth int `json:"initialHealth" binding:"omitempty,min=1000,max=50000"`
	RoundDurationSeconds int `json:"RoundDurationSeconds" binding:"omitempty,min=15,max=300"` // Optional, with defaults
}

type MultiplayerGameLocation struct {
	MultiplayerGameID uint `gorm:"primaryKey"`
	LocationID        uint `gorm:"primaryKey"`
	RoundNumber       int  `gorm:"not null"` // To enforce order
}

type MultiplayerRound struct {
	ID          uint      `gorm:"primaryKey"`
	SessionID   uint      `gorm:"not null;index"` // Link to the player's session
	RoundNumber int       `gorm:"not null"`       // Which round this guess is for (e.g., 1, 2, ...)
	LocationID  uint      `gorm:"not null"`       // The ID of the actual location for this round
	GuessLat    float64   // Player's guessed latitude
	GuessLng    float64   // Player's guessed longitude
	DistanceKm  float64   // Calculated distance
	Score       int       // Calculated score
	GuessedAt   time.Time `gorm:"autoCreateTime"` // When the guess was processed

	Session  MultiplayerSession `gorm:"foreignKey:SessionID"`  // Belongs To relationship
	Location Location           `gorm:"foreignKey:LocationID"` // Belongs To relationship
}
