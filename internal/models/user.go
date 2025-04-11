package models

import (
	"time" 

	"gorm.io/gorm"
)


type User struct {
	ID        uint   `gorm:"primaryKey"`
	Username  string `gorm:"size:50;not null;uniqueIndex" validate:"required,min=3,max=50"` 
	Email     string `gorm:"size:100;not null;uniqueIndex" validate:"required,email"`        
	PasswordHash string `gorm:"not null" validate:"required"`                             
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"` 

	
	
	
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