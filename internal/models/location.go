package models

import "gorm.io/gorm"

type Location struct {
	gorm.Model
	Latitude    float64 `gorm:"not null;index:idx_lat"`
	Longitude   float64 `gorm:"not null;index:idx_lng"`
	Description string `gorm: index:idx_description"`
	Country     string `gorm:"index:idx_country"`
	Region      string `gorm:"index:idx_region"`

	// Composite index for lat/lng pairs
	// This is especially useful for geographic queries
	// that need to look up locations by both coordinates
	_ int `gorm:"index:idx_lat_lng,priority:1,columns:latitude,longitude"`
}
