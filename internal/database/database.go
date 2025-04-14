package database

import (
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"geoguessr-backend/internal/models"
)

var DB *gorm.DB

func Connect() error {

	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}
	dbPort := os.Getenv("DB_PORT")
	if dbPort == "" {
		dbPort = "5432"
	}
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "postgres"
	}
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		log.Println("WARNING: DB_PASSWORD environment variable not set.")

		dbPassword = "mysecretpassword"
	}
	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "geoguessr"
	}
	dbSSLMode := os.Getenv("DB_SSLMODE")
	if dbSSLMode == "" {
		dbSSLMode = "disable"
	}

	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=UTC",
		dbHost, dbUser, dbPassword, dbName, dbPort, dbSSLMode)

	logLevel := logger.Warn // <<< Change from logger.Info to logger.Warn
	if os.Getenv("GORM_LOG_LEVEL") == "info" {
		logLevel = logger.Info // Allow overriding via env var if needed
	}

	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
		logger.Config{
			SlowThreshold:             time.Second * 2, // Slow SQL threshold (optional)
			LogLevel:                  logLevel,        // Set the desired log level
			IgnoreRecordNotFoundError: true,            // Don't log ErrRecordNotFound errors (usually expected)
			ParameterizedQueries:      false,           // Don't include params in Info level logs (optional)
			Colorful:                  true,            // Enable color (optional)
		},
	)
	// ---------------------------

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: newLogger, // <<< Use the configured logger
	})

	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	log.Println("Database connection established successfully.")
	log.Println("Running auto migrations...")
	err = DB.AutoMigrate(
		&models.Location{},
		&models.User{},
		&models.Game{},
		&models.Round{},
	)
	if err != nil {
		return fmt.Errorf("failed to auto migrate database: %w", err)
	}
	log.Println("Auto migrations completed.")

	return nil
}

func GetDB() *gorm.DB {
	return DB
}
