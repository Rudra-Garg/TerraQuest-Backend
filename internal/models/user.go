package models

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID               uint   `gorm:"primaryKey"`
	Username         string `gorm:"size:50;not null;uniqueIndex" validate:"required,min=3,max=50"`
	Email            string `gorm:"size:100;not null;uniqueIndex" validate:"required,email"`
	PasswordHash     string `gorm:"not null" validate:"required"`
	RecoveryCodeHash string `gorm:"not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}

type RegisterInput struct {
	Username string `json:"username" validate:"required,min=3,max=50"`
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6,max=72"`
}

type LoginInput struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type ForgotPasswordInput struct {
	Email        string `json:"email" validate:"required,email"`
	RecoveryCode string `json:"recoveryCode" validate:"required"`
	NewPassword  string `json:"newPassword" validate:"required,min=6"`
}

type UserProfileResponse struct {
	ID          uint      `json:"id"`
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	GamesPlayed int64     `json:"gamesPlayed"`
	HighScore   int       `json:"highScore"`
	TotalScore  int64     `json:"totalScore"`
	CreatedAt   time.Time `json:"createdAt"`
}

type LeaderboardEntry struct {
	ID            uint      `json:"id"`
	Username      string    `json:"username"`
	GamesPlayed   int64     `json:"gamesPlayed"`
	AvgScore      int64     `json:"avgScore"`
	BestScore     int       `json:"bestScore"`
	TotalPoints   int64     `json:"totalPoints"`
	JoinDate      time.Time `json:"joinDate"`
	IsCurrentUser bool      `json:"isCurrentUser,omitempty"`
}

type GameHistoryRoundResponse struct {
	RoundNumber int     `json:"roundNumber"`
	LocationID  uint    `json:"locationId"`
	GuessLat    float64 `json:"guessLat"`
	GuessLng    float64 `json:"guessLng"`
	ActualLat   float64 `json:"actualLat"`
	ActualLng   float64 `json:"actualLng"`
	DistanceKm  float64 `json:"distanceKm"`
	Score       int     `json:"score"`
}

type GameHistoryEntry struct {
	ID           uint                       `json:"id"`
	TotalScore   int                        `json:"totalScore"`
	RoundsPlayed int                        `json:"roundsPlayed"`
	CreatedAt    time.Time                  `json:"createdAt"`
	Rounds       []GameHistoryRoundResponse `json:"rounds"`
}
