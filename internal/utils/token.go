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


func InitializeJWT() {
	secret := os.Getenv("JWT_SECRET_KEY")
	if secret == "" {
		log.Fatal("FATAL: JWT_SECRET_KEY environment variable not set!")
	}
	jwtSecretKey = []byte(secret)
	log.Println("JWT Secret Key loaded.")
}


func GenerateToken(userID uint) (string, error) {
	
	
	
	expirationTime := time.Now().Add(24 * time.Hour) 
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     expirationTime.Unix(),
		"iat":     time.Now().Unix(),
		"iss":     "terraquest-backend", 
	}

	
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	
	tokenString, err := token.SignedString(jwtSecretKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}


func ValidateToken(tokenString string) (*jwt.Token, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		
		return jwtSecretKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err) 
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return token, nil
}


func ExtractToken(c *gin.Context) string {
	bearerToken := c.Request.Header.Get("Authorization")
	
	parts := strings.Split(bearerToken, " ")
	if len(parts) == 2 && parts[0] == "Bearer" {
		return parts[1]
	}
	return ""
}


func ExtractUserIDFromToken(token *jwt.Token) (uint, error) {
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return 0, fmt.Errorf("invalid token claims")
	}

	
	userIDFloat, ok := claims["user_id"].(float64)
	if !ok {
		return 0, fmt.Errorf("user_id claim is not a valid number")
	}

	
	return uint(userIDFloat), nil
}


func ExtractUserID(c *gin.Context) (uint, error) {
	tokenString := ExtractToken(c)
	if tokenString == "" {
		return 0, fmt.Errorf("authorization token not provided")
	}

	token, err := ValidateToken(tokenString)
	if err != nil {
		return 0, err 
	}

	return ExtractUserIDFromToken(token)
}