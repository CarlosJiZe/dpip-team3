package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

	// OriginalCount lleva la cuenta de originales subidos, para calcular
	// progreso y estado. No se expone en el JSON.
	OriginalCount int `json:"-"`
}

type Image struct {
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Type       string `json:"type"`
	// Seq es el indice del frame original (0, 1, 2, ...). Permite reconstruir
	// el orden del video al final. Se propaga del original a su filtrada.
	Seq int `json:"seq"`
}

// JobPayload es lo que la API manda al Controller para crear un job.
type JobPayload struct {
	JobID      string `json:"job_id"`
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Filter     string `json:"filter"`
	Token      string `json:"token"`
	Seq        int    `json:"seq"`
}

// ---- Almacenamiento en memoria con mutex ----
var (
	activeTokens = map[string]string{}
	workloads    = map[string]*Workload{}
	images       = map[string]*Image{}
	mu           sync.Mutex
	jobQueue     = make(chan JobPayload, 20000)
)

// ---- Helpers ----

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getActiveWorkloadNames() []string {
	names := []string{}
	for _, w := range workloads {
		names = append(names, w.WorkloadName)
	}
	return names
}

// updateWorkloadProgress recalcula running_jobs y status de un workload.
// Debe llamarse con el mutex 'mu' ya tomado.
func updateWorkloadProgress(w *Workload) {
	filtered := len(w.FilteredImages)
	w.RunningJobs = w.OriginalCount - filtered
	if w.RunningJobs < 0 {
		w.RunningJobs = 0
	}
	switch {
	case w.OriginalCount == 0:
		w.Status = "scheduling"
	case filtered >= w.OriginalCount:
		w.Status = "completed"
	default:
		w.Status = "running"
	}
}

func notifyController(imageID, workloadID, filter, token string, seq int) {
	jobQueue <- JobPayload{
		JobID:      generateID(),
		ImageID:    imageID,
		WorkloadID: workloadID,
		Filter:     filter,
		Token:      token,
		Seq:        seq,
	}
}

func processJobQueue() {
	for job := range jobQueue {
		data, _ := json.Marshal(job)
		http.Post("http://localhost:8081/jobs", "application/json", bytes.NewReader(data))
	}
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
		mu.Lock()
		username, exists := activeTokens[token]
		mu.Unlock()
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
	os.MkdirAll("storage", os.ModePerm)

	go processJobQueue()

	router := gin.Default()

	router.POST("/login", loginHandler)

	protected := router.Group("/")
	protected.Use(authMiddleware())
	{
		protected.DELETE("/logout", logoutHandler)
		protected.GET("/status", statusHandler)
		protected.POST("/workloads", createWorkloadHandler)
		protected.GET("/workloads/:workload_id", getWorkloadHandler)
		protected.GET("/images", listImagesHandler)
		protected.POST("/images", uploadImageHandler)
		protected.GET("/images/:image_id", downloadImageHandler)
	}

	router.Run(":8080")
}

// ---- Handlers ----

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
	mu.Lock()
	activeTokens[token] = username
	mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"user": username, "token": token})
}

func logoutHandler(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	mu.Lock()
	delete(activeTokens, token)
	mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"logout_message": "logged out successfully"})
}

func statusHandler(c *gin.Context) {
	mu.Lock()
	names := getActiveWorkloadNames()
	mu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"system_name":      "DPIP Team 3",
		"server_time":      time.Now().Format(time.RFC3339),
		"active_workloads": names,
	})
}

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

	os.MkdirAll(filepath.Join("storage", body.WorkloadName), os.ModePerm)

	workload := &Workload{
		WorkloadID:     generateID(),
		Filter:         body.Filter,
		WorkloadName:   body.WorkloadName,
		Status:         "scheduling",
		RunningJobs:    0,
		FilteredImages: []string{},
		OriginalCount:  0,
	}
	mu.Lock()
	workloads[workload.WorkloadID] = workload
	mu.Unlock()
	c.JSON(http.StatusOK, workload)
}

func getWorkloadHandler(c *gin.Context) {
	workloadID := c.Param("workload_id")
	mu.Lock()
	workload, exists := workloads[workloadID]
	mu.Unlock()
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	c.JSON(http.StatusOK, workload)
}

func listImagesHandler(c *gin.Context) {
	mu.Lock()
	imageList := []*Image{}
	for _, img := range images {
		imageList = append(imageList, img)
	}
	mu.Unlock()
	c.JSON(http.StatusOK, imageList)
}

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

	// seq: indice del frame original. Opcional; si no viene se asume 0.
	seq := 0
	if seqStr := c.PostForm("seq"); seqStr != "" {
		if v, errConv := strconv.Atoi(seqStr); errConv == nil {
			seq = v
		}
	}

	mu.Lock()
	workload, exists := workloads[workloadID]
	mu.Unlock()
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
		Seq:        seq,
	}

	mu.Lock()
	images[imageID] = image
	if imageType == "original" {
		workload.OriginalCount++
	} else if imageType == "filtered" {
		workload.FilteredImages = append(workload.FilteredImages, imageID)
	}
	updateWorkloadProgress(workload)
	mu.Unlock()

	if imageType == "original" {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		go notifyController(image.ImageID, workloadID, workload.Filter, token, seq)
	}

	c.JSON(http.StatusOK, image)
}

func downloadImageHandler(c *gin.Context) {
	imageID := c.Param("image_id")
	mu.Lock()
	image, exists := images[imageID]
	mu.Unlock()
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
		return
	}
	mu.Lock()
	workload, exists := workloads[image.WorkloadID]
	mu.Unlock()
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "workload not found"})
		return
	}
	imagePath := filepath.Join("storage", workload.WorkloadName, imageID+".png")
	c.File(imagePath)
}
