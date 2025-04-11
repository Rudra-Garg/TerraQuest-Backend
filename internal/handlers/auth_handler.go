
package handlers

import (
	"log"
	"net/http"
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

	
	newUser := models.User{
		Username:     input.Username,
		Email:        input.Email,
		PasswordHash: string(hashedPassword),
	}

	
	result := db.Create(&newUser)
	if result.Error != nil {
		log.Printf("Register Error - DB Create: %v", result.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register user"})
		return
	}

	log.Printf("User registered successfully: %s (%s)", newUser.Username, newUser.Email)
	
	c.JSON(http.StatusCreated, gin.H{
		"message": "User registered successfully",
		"user": gin.H{
			"id":       newUser.ID,
			"username": newUser.Username,
			"email":    newUser.Email,
		},
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