// internal/middleware/auth.go
package middleware

import (
	"log"
	"net/http"

	"geoguessr-backend/internal/utils" // Adjust path

	"github.com/gin-gonic/gin"
)

// AuthRequired is a middleware function to protect routes
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := utils.ExtractToken(c) // Get token from "Bearer <token>" header
		if tokenString == "" {
			log.Println("Auth Middleware: No token provided")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
			return
		}

		token, err := utils.ValidateToken(tokenString) // Validate signature, expiry, etc.
		if err != nil {
			log.Printf("Auth Middleware: Invalid token: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}

		// Token is valid, extract user ID
		userID, err := utils.ExtractUserIDFromToken(token)
		if err != nil {
			log.Printf("Auth Middleware: Failed to extract userID from valid token: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
			return
		}

		// Set user ID in the context for subsequent handlers to use
		c.Set("userID", userID)
		log.Printf("Auth Middleware: User %d authenticated", userID)

		// Proceed to the next handler
		c.Next()
	}
}
