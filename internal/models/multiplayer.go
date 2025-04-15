// internal/models/multiplayer.go
package models

import (
	"time"
)

// MultiplayerGame represents a game with multiple participants
type MultiplayerGame struct {
	ID            uint      `gorm:"primaryKey"`
	HostUserID    uint      `gorm:"not null;index"`
	GameCode      string    `gorm:"uniqueIndex;size:8"` // For joining games
	Status        string    `gorm:"size:20;not null"` // "waiting", "in_progress", "completed"
	MaxPlayers    int       `gorm:"not null;default:4"`
	RoundsTotal   int       `gorm:"not null;default:5"`
	CurrentRound  int       `gorm:"not null;default:0"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	
	// Same locations for all players
	Locations     []Location `gorm:"many2many:multiplayer_game_locations;"`
	// Player sessions
	PlayerSessions []MultiplayerSession `gorm:"foreignKey:GameID"`
}

// MultiplayerSession represents a player's session in a multiplayer game
type MultiplayerSession struct {
	ID          uint      `gorm:"primaryKey"`
	GameID      uint      `gorm:"not null;index"`
	UserID      uint      `gorm:"not null;index"`
	IsHost      bool      `gorm:"not null;default:false"`
	IsReady     bool      `gorm:"not null;default:false"`
	TotalScore  int       `gorm:"not null;default:0"`
	IsActive    bool      `gorm:"not null;default:true"`
	JoinedAt    time.Time
	
	User        User
	Game        MultiplayerGame
	Rounds      []MultiplayerRound `gorm:"foreignKey:SessionID"`
}

// MultiplayerRound represents a single round for a player in a multiplayer game
type MultiplayerRound struct {
	ID           uint    `gorm:"primaryKey"`
	SessionID    uint    `gorm:"not null;index"`
	RoundNumber  int     `gorm:"not null"`
	LocationID   uint    `gorm:"not null"`
	GuessLat     float64
	GuessLng     float64
	DistanceKm   float64
	Score        int
	GuessedAt    time.Time
} 