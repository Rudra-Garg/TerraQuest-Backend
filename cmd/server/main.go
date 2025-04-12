package main

import (
	"log"
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"github.com/go-openapi/runtime/middleware"

	_ "geoguessr-backend/docs" // for swagger docs
	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/handlers"
	internalMiddleware "geoguessr-backend/internal/middleware"
	"geoguessr-backend/internal/utils"
)

// @title           TerraQuest Backend API
// @version         1.0
// @description     API Server for the TerraQuest GeoGuessr clone game.
// @termsOfService  http://swagger.io/terms/  <-- Update later

// @contact.name   API Support
// @contact.url    http://www.example.com/support <-- Update later
// @contact.email  support@example.com <-- Update later

// @license.name  Apache 2.0  <-- Or your chosen license
// @license.url   http://www.apache.org/licenses/LICENSE-2.0.html

// @host      localhost:8080
// @BasePath  /api/v1

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token.

func main() {
	log.Println("Starting GeoGuessr Backend Server...")

	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, using system environment variables.")
	}

	utils.InitializeJWT()

	err = database.Connect()
	if err != nil {
		log.Fatalf("Could not connect to the database: %v", err)
	}

	// Initialize WebSocket hub
	// hub := websocket.NewHub()
	// go hub.Run()

	router := gin.Default()

	config := cors.DefaultConfig()

	config.AllowOrigins = []string{"http://localhost:5173"}
	config.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization"}

	router.Use(cors.New(config))

	router.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	log.Println("Swagger UI available at http://localhost:8080/docs/index.html")

	opts := middleware.RedocOpts{
		SpecURL: "/docs/doc.json",
		Path:    "/redoc",
		Title:   "TerraQuest API Docs (ReDoc)",
	}
	redocHandler := middleware.Redoc(opts, nil)
	router.GET("/redoc", gin.WrapH(redocHandler))
	log.Println("ReDoc UI available at http://localhost:8080/redoc")

	api := router.Group("/api/v1")
	{
		authGroup := api.Group("/auth")
		{
			authGroup.POST("/register", handlers.Register)
			authGroup.POST("/login", handlers.Login)
		}

		// Game routes - apply auth middleware here
		gameGroup := api.Group("/game")
		// Apply AuthRequired middleware to all routes within this group
		gameGroup.Use(internalMiddleware.AuthRequired()) // <<< APPLY MIDDLEWARE
		{
			gameGroup.GET("/start", handlers.StartGame)
			gameGroup.POST("/finish", handlers.FinishGame)

			// // WebSocket endpoint for multiplayer
			// gameGroup.GET("/ws", func(c *gin.Context) {
			// 	websocket.ServeWs(hub, c)
			// })
		}

	}

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	port := ":8080"
	log.Printf("Server listening on port %s\n", port)
	err = router.Run(port)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
