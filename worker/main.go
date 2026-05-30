package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	mangos "go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/push"
	"go.nanomsg.org/mangos/v3/protocol/sub"
	_ "go.nanomsg.org/mangos/v3/transport/all"
)

// ---- Estado global ----
var (
	workerName  string
	apiEndpoint string
	runningJobs int
	mu          sync.Mutex
	pushSock    mangos.Socket

	// MaxConnsPerHost pone un tope duro de conexiones simultaneas hacia la API,
	// evitando el agotamiento de puertos efimeros.
	httpClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			MaxConnsPerHost:     50,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second,
	}
)

// ---- Tipos RPC ----
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

// ---- Servicio RPC ----
type WorkerService struct{}

func (w *WorkerService) ProcessJob(args *JobArgs, reply *JobReply) error {
	log.Printf("[JOB] Starting job=%s filter=%s seq=%d image=%s", args.JobID, args.Filter, args.Seq, args.ImageID)

	mu.Lock()
	runningJobs++
	mu.Unlock()
	defer func() {
		mu.Lock()
		runningJobs--
		mu.Unlock()
	}()

	// 1. Descargar imagen original
	imgData, err := downloadImage(args.ImageID, args.Token)
	if err != nil {
		reply.Success = false
		reply.Message = "failed to download: " + err.Error()
		return nil
	}

	// 2. Guardar en archivo temporal (cross-platform: %TEMP% en Windows, /tmp en Linux/Mac)
	tmpInput := filepath.Join(os.TempDir(), args.ImageID+"_input.png")
	tmpOutput := filepath.Join(os.TempDir(), args.ImageID+"_output.png")
	defer os.Remove(tmpInput)
	defer os.Remove(tmpOutput)

	if err := os.WriteFile(tmpInput, imgData, 0644); err != nil {
		reply.Success = false
		reply.Message = "failed to save temp file: " + err.Error()
		return nil
	}

	// 3. Aplicar filtro
	if err := applyFilter(tmpInput, tmpOutput, args.Filter); err != nil {
		reply.Success = false
		reply.Message = "failed to apply filter: " + err.Error()
		return nil
	}

	// 4. Subir imagen filtrada, conservando la seq del frame original
	if err := uploadFilteredImage(tmpOutput, args.WorkloadID, args.Token, args.Seq); err != nil {
		reply.Success = false
		reply.Message = "failed to upload: " + err.Error()
		return nil
	}

	reply.Success = true
	reply.Message = "job completed successfully"
	log.Printf("[JOB] Completed job=%s seq=%d", args.JobID, args.Seq)
	return nil
}

// downloadImage descarga la imagen original desde la API
func downloadImage(imageID, token string) ([]byte, error) {
	url := fmt.Sprintf("%s/images/%s", apiEndpoint, imageID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// applyFilter aplica grayscale o blur a la imagen
func applyFilter(inputPath, outputPath, filter string) error {
	img, err := imaging.Open(inputPath)
	if err != nil {
		return err
	}

	switch filter {
	case "grayscale":
		result := imaging.Grayscale(img)
		return imaging.Save(result, outputPath)
	case "blur":
		result := imaging.Blur(img, 3.0)
		return imaging.Save(result, outputPath)
	default:
		result := imaging.Grayscale(img)
		return imaging.Save(result, outputPath)
	}
}

// uploadFilteredImage sube la imagen filtrada a la API, incluyendo la seq.
func uploadFilteredImage(filePath, workloadID, token string, seq int) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, _ := writer.CreateFormFile("data", "filtered.png")
	io.Copy(part, file)
	writer.WriteField("workload_id", workloadID)
	writer.WriteField("type", "filtered")
	writer.WriteField("seq", strconv.Itoa(seq))
	writer.Close()

	req, _ := http.NewRequest("POST", apiEndpoint+"/images", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	// Drenamos siempre el body antes de cerrar para reutilizar la conexion.
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %s", string(body))
	}
	return nil
}

// sendMessage manda un mensaje al controller
func sendMessage(msgType string, payload interface{}) {
	payloadBytes, _ := json.Marshal(payload)
	msg := struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}{
		Type:    msgType,
		Payload: payloadBytes,
	}
	data, _ := json.Marshal(msg)
	pushSock.Send(data)
}

// sendStatusLoop manda actualizaciones de status cada 5 segundos
func sendStatusLoop() {
	type StatusPayload struct {
		Name        string  `json:"name"`
		CPUUsage    float64 `json:"cpu_usage"`
		MemUsage    float64 `json:"mem_usage"`
		RunningJobs int     `json:"running_jobs"`
	}

	for range time.Tick(5 * time.Second) {
		cpuPercent, _ := cpu.Percent(time.Second, false)
		vmStat, _ := mem.VirtualMemory()

		mu.Lock()
		jobs := runningJobs
		mu.Unlock()

		cpuVal := 0.0
		if len(cpuPercent) > 0 {
			cpuVal = cpuPercent[0]
		}

		sendMessage("status", StatusPayload{
			Name:        workerName,
			CPUUsage:    cpuVal,
			MemUsage:    vmStat.UsedPercent,
			RunningJobs: jobs,
		})
	}
}

func main() {
	controllerHost := flag.String("controller", "localhost:7777", "Controller host:port (PULL port)")
	pubPort := flag.String("pub-port", "", "Controller PUB port (default: PULL port + 1)")
	name := flag.String("worker-name", "worker1", "Worker name")
	tagsStr := flag.String("tags", "cpu", "Tags separated by commas")
	flag.Parse()

	workerName = *name
	tags := strings.Split(*tagsStr, ",")

	// Calcular la dirección del socket PUB del controller (PULL port + 1 por defecto)
	var pubAddr string
	if *pubPort != "" {
		host := (*controllerHost)[:strings.LastIndex(*controllerHost, ":")]
		pubAddr = host + ":" + *pubPort
	} else {
		// Extraer host y puerto del flag --controller, sumar 1 al puerto
		lastColon := strings.LastIndex(*controllerHost, ":")
		host := (*controllerHost)[:lastColon]
		portStr := (*controllerHost)[lastColon+1:]
		portNum := 7778 // fallback seguro
		if p, err := strconv.Atoi(portStr); err == nil {
			portNum = p + 1
		}
		pubAddr = fmt.Sprintf("%s:%d", host, portNum)
	}

	// ---- PUSH socket: enviar mensajes al controller ----
	sock, err := push.NewSocket()
	if err != nil {
		log.Fatal("Error creating PUSH socket:", err)
	}
	if err := sock.Dial("tcp://" + *controllerHost); err != nil {
		log.Fatal("Error connecting to controller:", err)
	}
	pushSock = sock
	log.Printf("Connected to controller at %s", *controllerHost)

	// ---- SUB socket: recibir broadcasts del controller ----
	subSock, err := sub.NewSocket()
	if err != nil {
		log.Fatal("Error creating SUB socket:", err)
	}
	subSock.SetOption(mangos.OptionSubscribe, []byte(""))
	if err := subSock.Dial("tcp://" + pubAddr); err != nil {
		log.Fatal("Error connecting to PUB socket:", err)
	}

	// ---- Encontrar puerto libre para RPC ----
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("Error finding free port:", err)
	}
	rpcPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	workerAddress := fmt.Sprintf("localhost:%d", rpcPort)

	// ---- Registrarse con el controller ----
	type RegisterPayload struct {
		Name    string   `json:"name"`
		Address string   `json:"address"`
		Tags    []string `json:"tags"`
	}
	sendMessage("register", RegisterPayload{
		Name:    workerName,
		Address: workerAddress,
		Tags:    tags,
	})
	log.Printf("Registered as '%s' with RPC at %s", workerName, workerAddress)

	// ---- Escuchar info de la API desde el controller ----
	go func() {
		for {
			data, err := subSock.Recv()
			if err != nil {
				log.Println("Error receiving broadcast:", err)
				continue
			}
			var info map[string]string
			if err := json.Unmarshal(data, &info); err != nil {
				continue
			}
			if info["type"] == "api_info" {
				mu.Lock()
				apiEndpoint = info["endpoint"]
				mu.Unlock()
				log.Printf("Received API info: endpoint=%s", apiEndpoint)
			}
		}
	}()

	// ---- Iniciar servidor RPC ----
	rpc.Register(&WorkerService{})
	rpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", rpcPort))
	if err != nil {
		log.Fatal("Error starting RPC server:", err)
	}
	log.Printf("RPC server listening on :%d", rpcPort)
	go rpc.Accept(rpcListener)

	// ---- Mandar status al controller cada 5 segundos ----
	sendStatusLoop()
}
