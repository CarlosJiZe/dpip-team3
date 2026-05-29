package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ---- Usuarios hardcodeados ----
var users = map[string]string{
	"Carlos": "carlos123",
	"Demian": "demian123",
	"Fong":   "fong123",
	"Loco":   "loco123",
}

// ---- Estructuras ----
type Workload struct {
	WorkloadID     string   `json:"workload_id"`
	Filter         string   `json:"filter"`
	WorkloadName   string   `json:"workload_name"`
	Status         string   `json:"status"`
	RunningJobs    int      `json:"running_jobs"`
	FilteredImages []string `json:"filtered_images"`
}

type Image struct {
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Type       string `json:"type"`
}

// ---- Almacenamiento en memoria ----
var activeTokens = map[string]string{}
var workloads = map[string]*Workload{}
var images = map[string]*Image{}

// ---- Helpers ----
func generateID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func getActiveWorkloadNames() []string {
	names := []string{}
	for _, w := range workloads {
		names = append(names, w.WorkloadName)
	}
	return names
}

// ---- Auth middleware ----
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
		c.Set("username", username)
		c.Next()
	}
}

// ---- Main ----
func main() {
	// Crear directorio para guardar imágenes
	os.MkdirAll("storage", os.ModePerm)

	router := gin.Default()

	// Endpoint público
	router.POST("/login", loginHandler)

	// Endpoints protegidos
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

// ---- Handlers ----

// POST /login
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
	token := generateID()
	activeTokens[token] = username
	c.JSON(http.StatusOK, gin.H{"user": username, "token": token})
}

// DELETE /logout
func logoutHandler(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	delete(activeTokens, token)
	c.JSON(http.StatusOK, gin.H{"logout_message": "logged out successfully"})
}

// GET /status
func statusHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"system_name":      "DPIP Team 3",
		"server_time":      time.Now().Format(time.RFC3339),
		"active_workloads": getActiveWorkloadNames(),
	})
}

// POST /workloads
func createWorkloadHandler(c *gin.Context) {
	var body struct {
		Filter       string `json:"filter"`
		WorkloadName string `json:"workload_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if body.Filter != "grayscale" && body.Filter != "blur" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filter must be grayscale or blur"})
		return
	}
	if body.WorkloadName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workload_name is required"})
		return
	}

	// Crear directorio para las imágenes del workload
	os.MkdirAll(filepath.Join("storage", body.WorkloadName), os.ModePerm)

	workload := &Workload{
		WorkloadID:     generateID(),
		Filter:         body.Filter,
		WorkloadName:   body.WorkloadName,
		Status:         "scheduling",
		RunningJobs:    0,
		FilteredImages: []string{},
	}
	workloads[workload.WorkloadID] = workload
	c.JSON(http.StatusOK, workload)
}

// GET /workloads/:workload_id
func getWorkloadHandler(c *gin.Context) {
	workloadID := c.Param("workload_id")
	workload, exists := workloads[workloadID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	c.JSON(http.StatusOK, workload)
}

// POST /images
func uploadImageHandler(c *gin.Context) {
	file, err := c.FormFile("data")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "image file is required"})
		return
	}
	workloadID := c.PostForm("workload_id")
	imageType := c.PostForm("type")

	if workloadID == "" || imageType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workload_id and type are required"})
		return
	}
	workload, exists := workloads[workloadID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}

	imageID := generateID()
	savePath := filepath.Join("storage", workload.WorkloadName, imageID+".png")

	if err := c.SaveUploadedFile(file, savePath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save image"})
		return
	}

	image := &Image{
		ImageID:    imageID,
		WorkloadID: workloadID,
		Type:       imageType,
	}
	images[imageID] = image

	c.JSON(http.StatusOK, image)
}

// GET /images/:image_id
func downloadImageHandler(c *gin.Context) {
	imageID := c.Param("image_id")
	image, exists := images[imageID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
		return
	}
	workload, exists := workloads[image.WorkloadID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	imagePath := filepath.Join("storage", workload.WorkloadName, imageID+".png")
	c.File(imagePath)
}
