package models

import "gorm.io/gorm"


type Location struct {
	gorm.Model        
	Latitude   float64 `gorm:"not null"`
	Longitude  float64 `gorm:"not null"`
	Description string  
	Country    string  
	Region     string  
	
}




