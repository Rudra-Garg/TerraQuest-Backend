package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"strings"

	"geoguessr-backend/internal/database"
	"geoguessr-backend/internal/models"
	"geoguessr-backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var validate = validator.New()

func generateRandomCode(n int) string {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "RECOVERY-123-ABC" // Fallback
	}
	return hex.EncodeToString(bytes)[:n]
}

// Register godoc
// @Summary      Register a new user
// @Description  Creates a new user account with username, email, and password.
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        userInput body models.RegisterInput true "User Registration Info"
// @Success      201  {object}  map[string]interface{}  "User registered successfully (returns basic user info)"
// @Failure      400  {object}  map[string]string "Invalid input format or validation failed"
// @Failure      409  {object}  map[string]string "Conflict: Username or Email already exists"
// @Failure      500  {object}  map[string]string "Internal server error (hashing, DB create)"
// @Router       /auth/register [post]
func Register(c *gin.Context) {
	var input models.RegisterInput

	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("Register Error - Binding: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input format"})
		return
	}

	if err := validate.Struct(input); err != nil {
		log.Printf("Register Error - Validation: %v", err)

		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed: " + err.Error()})
		return
	}

	db := database.GetDB()

	var existingUser models.User
	err := db.Where("email = ? OR username = ?", input.Email, input.Username).First(&existingUser).Error
	if err == nil {

		errorMsg := "Conflict: "
		if strings.EqualFold(existingUser.Email, input.Email) {
			errorMsg += "Email already exists."
		} else {
			errorMsg += "Username already exists."
		}
		log.Printf("Register Error - Conflict: %s", errorMsg)
		c.JSON(http.StatusConflict, gin.H{"error": errorMsg})
		return
	} else if err != gorm.ErrRecordNotFound {

		log.Printf("Register Error - DB Check: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error checking user existence"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("Register Error - Hashing: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process password"})
		return
	}

	// Generate Recovery Code
	recoveryCode := strings.ToUpper(generateRandomCode(12))
	recoveryHash, _ := bcrypt.GenerateFromPassword([]byte(recoveryCode), bcrypt.DefaultCost)

	newUser := models.User{
		Username:         input.Username,
		Email:            input.Email,
		PasswordHash:     string(hashedPassword),
		RecoveryCodeHash: string(recoveryHash), // Save hashed version
	}

	if err := db.Create(&newUser).Error; err != nil { /* error handle */
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":      "User registered successfully",
		"recoveryCode": recoveryCode,
		"user":         gin.H{"id": newUser.ID, "username": newUser.Username},
	})
}

// Login godoc
// @Summary      Log in a user
// @Description  Authenticates a user with email and password, returns a JWT token upon success.
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        credentials body models.LoginInput true "User Login Credentials"
// @Success      200  {object}  map[string]interface{} "Login successful (returns JWT token and basic user info)"
// @Failure      400  {object}  map[string]string "Invalid input format or validation failed"
// @Failure      401  {object}  map[string]string "Invalid credentials (user not found or password mismatch)"
// @Failure      500  {object}  map[string]string "Internal server error (DB find, token generation)"
// @Router       /auth/login [post]
func Login(c *gin.Context) {
	var input models.LoginInput

	if err := c.ShouldBindJSON(&input); err != nil {
		log.Printf("Login Error - Binding: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input format"})
		return
	}

	if err := validate.Struct(input); err != nil {
		log.Printf("Login Error - Validation: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed: " + err.Error()})
		return
	}

	db := database.GetDB()
	var user models.User

	result := db.Where("email = ?", input.Email).First(&user)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			log.Printf("Login Error - User not found: %s", input.Email)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		} else {
			log.Printf("Login Error - DB Find: %v", result.Error)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error finding user"})
		}
		return
	}

	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password))
	if err != nil {

		log.Printf("Login Error - Password mismatch for user: %s", input.Email)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	token, err := utils.GenerateToken(user.ID)
	if err != nil {
		log.Printf("Login Error - Token Generation: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate login token"})
		return
	}

	log.Printf("User logged in successfully: %s (%d)", user.Username, user.ID)

	c.JSON(http.StatusOK, gin.H{
		"message": "Login successful",
		"token":   token,
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
	})
}

// Add the Reset function
func ResetPassword(c *gin.Context) {
	var input models.ForgotPasswordInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	db := database.GetDB()
	var user models.User
	if err := db.Where("email = ?", input.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Verify Recovery Code
	err := bcrypt.CompareHashAndPassword([]byte(user.RecoveryCodeHash), []byte(strings.ToUpper(input.RecoveryCode)))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid recovery code"})
		return
	}

	// Hash new password
	newHash, _ := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	db.Model(&user).Update("password_hash", string(newHash))

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successful"})
}

func GetProfile(c *gin.Context) {
	userIDAny, exists := c.Get("userID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User authentication not found"})
		return
	}

	userID, ok := userIDAny.(uint)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error processing user identity"})
		return
	}

	db := database.GetDB()

	var user models.User
	if err := db.First(&user, userID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error fetching user"})
		}
		return
	}

	type statsRow struct {
		GamesPlayed int
		TotalScore  int
		HighScore   int
	}

	var stats statsRow
	if err := db.Model(&models.Game{}).
		Select(
			"COUNT(*) as games_played, COALESCE(SUM(total_score), 0) as total_score, COALESCE(MAX(total_score), 0) as high_score",
		).
		Where("user_id = ?", userID).
		Scan(&stats).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error fetching user stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          user.ID,
		"username":    user.Username,
		"email":       user.Email,
		"createdAt":   user.CreatedAt,
		"gamesPlayed": stats.GamesPlayed,
		"totalScore":  stats.TotalScore,
		"highScore":   stats.HighScore,
	})
}

func GetLeaderboard(c *gin.Context) {
	db := database.GetDB()

	limitStr := c.DefaultQuery("limit", "50")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	type leaderboardRow struct {
		ID          uint   `json:"id"`
		Username    string `json:"username"`
		GamesPlayed int    `json:"gamesPlayed"`
		AvgScore    int    `json:"avgScore"`
		BestScore   int    `json:"bestScore"`
		TotalPoints int    `json:"totalPoints"`
		JoinDate    string `json:"joinDate"`
	}

	var rows []leaderboardRow
	if err := db.Table("users").
		Select(`
			users.id,
			users.username,
			COUNT(games.id) as games_played,
			COALESCE(ROUND(AVG(games.total_score)), 0) as avg_score,
			COALESCE(MAX(games.total_score), 0) as best_score,
			COALESCE(SUM(games.total_score), 0) as total_points,
			CAST(users.created_at AS TEXT) as join_date
		`).
		Joins("LEFT JOIN games ON games.user_id = users.id").
		Group("users.id, users.username, users.created_at").
		Order("total_points DESC, best_score DESC, games_played DESC, users.username ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch leaderboard"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"players": rows,
	})
}
