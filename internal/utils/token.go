package utils

import (
	"fmt"
	"log"
	"os"
	
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var jwtSecretKey []byte

// Initialize loads the JWT secret key from environment variables
func InitializeJWT() {
	secret := os.Getenv("JWT_SECRET_KEY")
	if secret == "" {
		log.Fatal("FATAL: JWT_SECRET_KEY environment variable not set!")
	}
	jwtSecretKey = []byte(secret)
	log.Println("JWT Secret Key loaded.")
}

// GenerateToken creates a new JWT for a given user ID
func GenerateToken(userID uint) (string, error) {
	// Set token claims
	// Standard claims: ExpiresAt, IssuedAt, etc.
	// Custom claims: user_id
	expirationTime := time.Now().Add(24 * time.Hour) // Token valid for 24 hours
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     expirationTime.Unix(),
		"iat":     time.Now().Unix(),
		"iss":     "terraquest-backend", // Optional: Issuer
	}

	// Create token with claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Sign the token with the secret key
	tokenString, err := token.SignedString(jwtSecretKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken parses and validates a JWT string
func ValidateToken(tokenString string) (*jwt.Token, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Check the signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		// Return the secret key for validation
		return jwtSecretKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err) // Covers expired, malformed, etc.
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return token, nil
}

// ExtractToken extracts the token string from the Authorization header
func ExtractToken(c *gin.Context) string {
	bearerToken := c.Request.Header.Get("Authorization")
	// Format: "Bearer <token>"
	parts := strings.Split(bearerToken, " ")
	if len(parts) == 2 && parts[0] == "Bearer" {
		return parts[1]
	}
	return ""
}

// ExtractUserIDFromToken retrieves the user ID from a validated token's claims
func ExtractUserIDFromToken(token *jwt.Token) (uint, error) {
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return 0, fmt.Errorf("invalid token claims")
	}

	// JWT standard often parses numbers as float64
	userIDFloat, ok := claims["user_id"].(float64)
	if !ok {
		return 0, fmt.Errorf("user_id claim is not a valid number")
	}

	// Convert float64 to uint
	return uint(userIDFloat), nil
}

// ExtractUserID helper function combines extraction and validation
func ExtractUserID(c *gin.Context) (uint, error) {
	tokenString := ExtractToken(c)
	if tokenString == "" {
		return 0, fmt.Errorf("authorization token not provided")
	}

	token, err := ValidateToken(tokenString)
	if err != nil {
		return 0, err // Propagate validation error
	}

	return ExtractUserIDFromToken(token)
}