package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Usuarios hardcodeados: username -> password
var users = map[string]string{
	"Carlos": "carlos123",
	"Demian": "demian123",
	"Fong":   "fong123",
	"Loco":   "loco123",
}

// Tokens activos en memoria: token -> username
var activeTokens = map[string]string{}

// generateToken genera un token aleatorio
func generateToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// authMiddleware verifica que el request tenga un token válido
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid token"})
			c.Abort()
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		username, exists := activeTokens[token]
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		// Guardamos el username en el contexto para usarlo después
		c.Set("username", username)
		c.Next()
	}
}

func main() {
	router := gin.Default()

	// Endpoints públicos (sin token)
	router.POST("/login", loginHandler)

	// Endpoints protegidos (requieren token)
	protected := router.Group("/")
	protected.Use(authMiddleware())
	{
		protected.DELETE("/logout", logoutHandler)
		protected.GET("/status", statusHandler)
		protected.POST("/workloads", createWorkloadHandler)
		protected.GET("/workloads/:workload_id", getWorkloadHandler)
		protected.POST("/images", uploadImageHandler)
		protected.GET("/images/:image_id", downloadImageHandler)
	}

	router.Run(":8080")
}

// POST /login — Basic Auth
func loginHandler(c *gin.Context) {
	username, password, ok := c.Request.BasicAuth()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "basic auth required"})
		return
	}

	expectedPassword, exists := users[username]
	if !exists || expectedPassword != password {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token := generateToken()
	activeTokens[token] = username

	c.JSON(http.StatusOK, gin.H{
		"user":  username,
		"token": token,
	})
}

// DELETE /logout — Bearer token
func logoutHandler(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	delete(activeTokens, token)
	c.JSON(http.StatusOK, gin.H{"logout_message": "logged out successfully"})
}

func statusHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "status - not implemented yet"})
}

func createWorkloadHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "create workload - not implemented yet"})
}

func getWorkloadHandler(c *gin.Context) {
	workloadID := c.Param("workload_id")
	c.JSON(http.StatusOK, gin.H{"message": "get workload - not implemented yet", "workload_id": workloadID})
}

func uploadImageHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "upload image - not implemented yet"})
}

func downloadImageHandler(c *gin.Context) {
	imageID := c.Param("image_id")
	c.JSON(http.StatusOK, gin.H{"message": "download image - not implemented yet", "image_id": imageID})
}
