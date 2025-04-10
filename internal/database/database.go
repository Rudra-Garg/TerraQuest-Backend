package database

import (
	"fmt"
	"log"
	"os" // To read environment variables

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger" // Optional: For more detailed GORM logging

    // Import models ONLY IF AutoMigrate is used here. Best practice is often
    // to handle migrations separately, but for simplicity now:
    "geoguessr-backend/internal/models" 

)

// DB is the global database connection pool instance
var DB *gorm.DB

// Connect initializes the database connection
func Connect() error {
	// Use environment variables for connection details (BEST PRACTICE)
	// Provide defaults for local development ease
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost" // Default if not set
	}
	dbPort := os.Getenv("DB_PORT")
	if dbPort == "" {
		dbPort = "5432" // Default if not set
	}
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "postgres" // Default user
	}
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		log.Println("WARNING: DB_PASSWORD environment variable not set.")
		// Set a default ONLY for local dev if using Docker command above
        // **REMOVE THIS IN PRODUCTION**
        dbPassword = "mysecretpassword"
	}
	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "geoguessr" // Default db name
	}
	dbSSLMode := os.Getenv("DB_SSLMODE")
	if dbSSLMode == "" {
		dbSSLMode = "disable" // Default for local dev
	}

	// Construct the Data Source Name (DSN)
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=UTC",
		dbHost, dbUser, dbPassword, dbName, dbPort, dbSSLMode)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info), // Log SQL queries
        // Add other GORM config here if needed
	})

	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	log.Println("Database connection established successfully.")

	// Auto Migration (Simple way to create tables, consider separate migration tools for production)
    log.Println("Running auto migrations...")
	err = DB.AutoMigrate(
		&models.Location{}, // Keep existing models
		&models.User{},     // <<< Add the User model
	)
    if err != nil {
        return fmt.Errorf("failed to auto migrate database: %w", err)
    }
    log.Println("Auto migrations completed.")


	return nil
}

// GetDB returns the initialized DB instance
func GetDB() *gorm.DB {
	return DB
}