package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/rpc"
	"sync"
	"time"
)

// ---- Estructuras ----

type WorkerInfo struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	CPUUsage    float64  `json:"cpu_usage"`
	MemUsage    float64  `json:"mem_usage"`
	RunningJobs int      `json:"running_jobs"`
	Tags        []string `json:"tags"`
}

type Job struct {
	JobID      string `json:"job_id"`
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Filter     string `json:"filter"`
	Status     string `json:"status"`
	Token      string `json:"token"`
	Seq        int    `json:"seq"`
}

type JobArgs struct {
	JobID      string `json:"job_id"`
	ImageID    string `json:"image_id"`
	WorkloadID string `json:"workload_id"`
	Filter     string `json:"filter"`
	Token      string `json:"token"`
	Seq        int    `json:"seq"`
}

type JobReply struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ---- Pool de conexiones RPC persistentes ----
var (
	rpcClients   = map[string]*rpc.Client{}
	rpcClientsMu sync.Mutex
)

// ---- Limite de concurrencia: evita lanzar cientos de jobs a la vez,
//
//	que era lo que agotaba los puertos efimeros (EAGAIN). ----
var jobSem chan struct{}

// ---- Control de reintentos: un job que falla N veces se marca como
//
//	"failed" en vez de reintentarse para siempre. ----
var (
	retryCounts   = map[string]int{}
	retryCountsMu sync.Mutex
)

const maxRetries = 5

func getRPCClient(address string) (*rpc.Client, error) {
	rpcClientsMu.Lock()
	defer rpcClientsMu.Unlock()

	if client, ok := rpcClients[address]; ok {
		return client, nil
	}

	client, err := rpc.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	rpcClients[address] = client
	log.Printf("[RPC] New persistent connection to %s", address)
	return client, nil
}

func removeRPCClient(address string) {
	rpcClientsMu.Lock()
	defer rpcClientsMu.Unlock()
	if client, ok := rpcClients[address]; ok {
		client.Close()
		delete(rpcClients, address)
		log.Printf("[RPC] Removed connection to %s", address)
	}
}

// ---- HTTP helpers ----

func getWorkers(controllerURL string) ([]WorkerInfo, error) {
	resp, err := http.Get(controllerURL + "/workers")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var workers []WorkerInfo
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &workers)
	return workers, nil
}

func getPendingJobs(controllerURL string) ([]Job, error) {
	resp, err := http.Get(controllerURL + "/jobs")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var jobs []Job
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &jobs)
	pending := []Job{}
	for _, j := range jobs {
		if j.Status == "pending" {
			pending = append(pending, j)
		}
	}
	return pending, nil
}

// updateJobStatus drena y cierra el body para poder reutilizar la conexion.
func updateJobStatus(controllerURL, jobID, status string) {
	url := fmt.Sprintf("%s/jobs/%s/status?status=%s", controllerURL, jobID, status)
	req, _ := http.NewRequest("PATCH", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[SCHEDULER] updateJobStatus error for %s: %v", jobID, err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func handleJobFailure(controllerURL, jobID string) {
	retryCountsMu.Lock()
	retryCounts[jobID]++
	count := retryCounts[jobID]
	retryCountsMu.Unlock()

	if count >= maxRetries {
		log.Printf("[SCHEDULER] Job=%s supero %d reintentos, marcado como failed", jobID, maxRetries)
		updateJobStatus(controllerURL, jobID, "failed")
		return
	}
	updateJobStatus(controllerURL, jobID, "pending")
}

func handleJobSuccess(controllerURL, jobID string) {
	retryCountsMu.Lock()
	delete(retryCounts, jobID)
	retryCountsMu.Unlock()
	updateJobStatus(controllerURL, jobID, "completed")
}

// ---- RPC call to worker usando conexion persistente ----

func sendJobToWorker(worker *WorkerInfo, job Job, controllerURL string) {
	log.Printf("[SCHEDULER] Sending job=%s seq=%d to worker=%s", job.JobID, job.Seq, worker.Name)

	client, err := getRPCClient(worker.Address)
	if err != nil {
		log.Printf("[SCHEDULER] Failed to connect to worker %s: %v", worker.Name, err)
		handleJobFailure(controllerURL, job.JobID)
		return
	}

	args := JobArgs{
		JobID:      job.JobID,
		ImageID:    job.ImageID,
		WorkloadID: job.WorkloadID,
		Filter:     job.Filter,
		Token:      job.Token,
		Seq:        job.Seq,
	}
	var reply JobReply

	err = client.Call("WorkerService.ProcessJob", &args, &reply)
	if err != nil {
		log.Printf("[SCHEDULER] RPC error for job=%s: %v - reconnecting", job.JobID, err)
		removeRPCClient(worker.Address)
		handleJobFailure(controllerURL, job.JobID)
		return
	}

	if reply.Success {
		log.Printf("[SCHEDULER] Job=%s completed: %s", job.JobID, reply.Message)
		handleJobSuccess(controllerURL, job.JobID)
	} else {
		log.Printf("[SCHEDULER] Job=%s failed: %s", job.JobID, reply.Message)
		handleJobFailure(controllerURL, job.JobID)
	}
}

// ---- Scheduling: round-robin entre workers, con tope de concurrencia ----

func schedulerLoop(controllerURL string, interval time.Duration) {
	log.Printf("Scheduler running, polling every %s", interval)
	workerIndex := 0

	for range time.Tick(interval) {
		jobs, err := getPendingJobs(controllerURL)
		if err != nil {
			log.Println("[SCHEDULER] Error getting jobs:", err)
			continue
		}
		if len(jobs) == 0 {
			continue
		}

		workers, err := getWorkers(controllerURL)
		if err != nil {
			log.Println("[SCHEDULER] Error getting workers:", err)
			continue
		}
		if len(workers) == 0 {
			continue
		}

		for _, job := range jobs {
			worker := workers[workerIndex%len(workers)]
			workerIndex++

			// Marcamos "running" antes de bloquearnos en el semaforo, asi el
			// siguiente tick no vuelve a tomar este job.
			updateJobStatus(controllerURL, job.JobID, "running")

			// Bloquea aqui si ya hay demasiados jobs en vuelo.
			jobSem <- struct{}{}
			go func(w WorkerInfo, j Job) {
				defer func() { <-jobSem }()
				sendJobToWorker(&w, j, controllerURL)
			}(worker, job)
		}
	}
}

func main() {
	controllerURL := flag.String("controller-url", "http://localhost:8081", "Controller HTTP URL")
	interval := flag.Duration("interval", 2*time.Second, "Polling interval")
	maxConcurrent := flag.Int("max-concurrent", 24, "Maximo de jobs en vuelo simultaneamente")
	flag.Parse()

	jobSem = make(chan struct{}, *maxConcurrent)

	log.Printf("Scheduler started, controller=%s, max-concurrent=%d", *controllerURL, *maxConcurrent)
	schedulerLoop(*controllerURL, *interval)
}
