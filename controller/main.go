package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.nanomsg.org/mangos/v3/protocol/pub"
	"go.nanomsg.org/mangos/v3/protocol/pull"

	_ "go.nanomsg.org/mangos/v3/transport/all"
)

// ---- Estructuras ----

type WorkerInfo struct {
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	CPUUsage    float64   `json:"cpu_usage"`
	MemUsage    float64   `json:"mem_usage"`
	RunningJobs int       `json:"running_jobs"`
	Tags        []string  `json:"tags"`
	LastSeen    time.Time `json:"last_seen"`
}

type Job struct {
	JobID      string `json:"job_id"`
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Filter     string `json:"filter"`
	Status     string `json:"status"`
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ---- Datastore en memoria ----

var (
	workers   = map[string]*WorkerInfo{}
	jobs      = map[string]*Job{}
	mu        sync.Mutex
	pubSocket interface {
		Send([]byte) error
	}
)

func main() {
	pullPort := flag.Int("pull-port", 5555, "Port to receive messages from workers")
	pubPort := flag.Int("pub-port", 5556, "Port to broadcast messages to workers")
	httpPort := flag.Int("http-port", 8081, "Port for internal HTTP API")
	flag.Parse()

	// ---- PULL socket: recibe mensajes de workers ----
	pullSock, err := pull.NewSocket()
	if err != nil {
		log.Fatal("Error creating PULL socket:", err)
	}
	if err := pullSock.Listen(fmt.Sprintf("tcp://0.0.0.0:%d", *pullPort)); err != nil {
		log.Fatal("Error listening on PULL socket:", err)
	}
	log.Printf("PULL socket listening on :%d", *pullPort)

	// ---- PUB socket: manda broadcasts a workers ----
	pubSock, err := pub.NewSocket()
	if err != nil {
		log.Fatal("Error creating PUB socket:", err)
	}
	if err := pubSock.Listen(fmt.Sprintf("tcp://0.0.0.0:%d", *pubPort)); err != nil {
		log.Fatal("Error listening on PUB socket:", err)
	}
	log.Printf("PUB socket listening on :%d", *pubPort)
	pubSocket = pubSock

	// ---- Goroutine: procesa mensajes entrantes de workers ----
	go handleWorkerMessages(pullSock)

	// ---- HTTP API interna ----
	startHTTPServer(*httpPort)
}

// handleWorkerMessages procesa los mensajes que llegan de los workers
func handleWorkerMessages(pullSock interface{ Recv() ([]byte, error) }) {
	for {
		data, err := pullSock.Recv()
		if err != nil {
			log.Println("Error receiving message:", err)
			continue
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Println("Error parsing message:", err)
			continue
		}

		mu.Lock()
		switch msg.Type {

		case "register":
			var worker WorkerInfo
			json.Unmarshal(msg.Payload, &worker)
			worker.LastSeen = time.Now()
			workers[worker.Name] = &worker
			log.Printf("[REGISTER] Worker '%s' at %s tags=%v", worker.Name, worker.Address, worker.Tags)

			// Mandar info de la API al worker recién registrado
			apiInfo, _ := json.Marshal(map[string]string{
				"type":     "api_info",
				"endpoint": "http://localhost:8080",
			})
			pubSocket.Send(apiInfo)

		case "status":
			var worker WorkerInfo
			json.Unmarshal(msg.Payload, &worker)
			if existing, ok := workers[worker.Name]; ok {
				existing.CPUUsage = worker.CPUUsage
				existing.MemUsage = worker.MemUsage
				existing.RunningJobs = worker.RunningJobs
				existing.LastSeen = time.Now()
				log.Printf("[STATUS] Worker '%s' CPU=%.1f%% MEM=%.1f%% Jobs=%d",
					worker.Name, worker.CPUUsage, worker.MemUsage, worker.RunningJobs)
			}
		}
		mu.Unlock()
	}
}

// startHTTPServer levanta la API interna del Controller
func startHTTPServer(port int) {
	router := gin.Default()

	// GET /workers — lista todos los workers registrados
	router.GET("/workers", func(c *gin.Context) {
		mu.Lock()
		defer mu.Unlock()
		workerList := []*WorkerInfo{}
		for _, w := range workers {
			workerList = append(workerList, w)
		}
		c.JSON(http.StatusOK, workerList)
	})

	// POST /jobs — la API envía un nuevo job para procesar
	router.POST("/jobs", func(c *gin.Context) {
		var job Job
		if err := c.ShouldBindJSON(&job); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job"})
			return
		}
		job.Status = "pending"
		mu.Lock()
		jobs[job.JobID] = &job
		mu.Unlock()
		log.Printf("[JOB] New job received: %s filter=%s", job.JobID, job.Filter)
		c.JSON(http.StatusOK, job)
	})

	// GET /jobs/:job_id — consulta el status de un job
	router.GET("/jobs/:job_id", func(c *gin.Context) {
		jobID := c.Param("job_id")
		mu.Lock()
		job, exists := jobs[jobID]
		mu.Unlock()
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
			return
		}
		c.JSON(http.StatusOK, job)
	})

	log.Printf("Controller HTTP API listening on :%d", port)
	router.Run(fmt.Sprintf(":%d", port))
}
