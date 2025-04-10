package main

import (
	"log"
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	// Import your packages
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/handlers"
	"geoguessr-backend/internal/utils" // <<< Import handlers
)

func main() {
	log.Println("Starting GeoGuessr Backend Server...")

	err := godotenv.Load() // Load environment variables from .env file
	if err != nil {
		log.Println("No .env file found, using system environment variables.")
	}
	
	utils.InitializeJWT()
	// 1. Initialize Database Connection
	err = database.Connect()
	if err != nil {
		log.Fatalf("Could not connect to the database: %v", err)
	}

	// 2. Create Gin router
	router := gin.Default()


	// --- Configure CORS ---
	// Allow requests specifically from your frontend development server
	config := cors.DefaultConfig()
	// config.AllowAllOrigins = true // Less secure, okay for quick local test but specify origin below
	config.AllowOrigins = []string{"http://localhost:5173"} // <<< Your frontend origin
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} // Allowed methods
	config.AllowHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization"} // Allowed headers
	// config.ExposeHeaders = []string{"Content-Length"} // Optional: Headers frontend can access
	// config.AllowCredentials = true // Optional: If you need cookies/auth headers

	router.Use(cors.New(config)) // <<< Apply CORS middleware GLOBALLY
	// -----------------------


	// --- API Routes ---
	// Group API routes under /api/v1
	api := router.Group("/api/v1")
	{
		// Health check/ping remains accessible
		// router.GET("/ping", func(c *gin.Context) { ... }) // Or move ping inside api group?

		gameGroup := api.Group("/game")
		{
			// Route to start a new game and get locations
			gameGroup.GET("/start", handlers.StartGame) // <<< Register the handler

			// Add other game-related routes here later (e.g., POST /submit-guess)
		}

		// Add other groups later (e.g., /users, /leaderboard)
	}

	// Add the original ping route outside the API group if desired
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})


	// 4. Start the server
	port := ":8080" // Ensure this matches frontend expectation or use env var
	log.Printf("Server listening on port %s\n", port)
	err = router.Run(port)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}