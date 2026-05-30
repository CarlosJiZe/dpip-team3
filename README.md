# DPIP — Distributed and Parallel Image Processing

A distributed image processing system built in Go that applies filters (grayscale, blur) to images across multiple worker nodes. Clients interact with a REST API; a master node (API + Controller + Scheduler) orchestrates the work and delegates processing to one or more workers via RPC.

**Team 3**
- Demián Velasco Gómez Llanos
- Carlos Jiménez Zepeda
- Jorge Alberto Fong Álvarez

---

## System Architecture

```
Clients (HTTP/JSON)
       │
       ▼
┌─────────────────────────────────┐
│           Master Node           │
│  ┌─────┐ ┌────────┐ ┌───────┐  │
│  │ API │ │  Ctrl  │ │ Sched │  │
│  │:8080│ │  :8081 │ │       │  │
│  └──┬──┘ └───┬────┘ └───┬───┘  │
└─────┼────────┼──────────┼──────┘
      │    PUSH/PUB       │ RPC
      │    (nanomsg)      │
      └───────────────────┼──────▶ Worker 1
                          └──────▶ Worker 2..N
```

- **API** (`:8080`) — REST interface for clients. Handles auth, workloads, and image upload/download.
- **Controller** (`:8081`) — Internal HTTP API. Manages worker registry and job queue. Communicates with workers via nanomsg PUSH/PUB sockets on ports `:7777` / `:7778`.
- **Scheduler** — Polls the controller for pending jobs and dispatches them to workers via Go's native RPC.
- **Worker** — Connects to the controller, receives jobs via RPC, downloads the original image from the API, applies the filter, and uploads the result back.

---

## Prerequisites

| Tool | Version |
|------|---------|
| Go | 1.25.6 or later |
| Python | 3.8+ (optional, for test scripts) |
| pip packages | `requests`, `opencv-python` (optional) |

---

## Installation

```bash
git clone https://github.com/CarlosJiZe/dpip-team3.git
cd dpip-team3
go mod download
```

---

## Running the System

The system requires **four separate terminals**, started in this exact order:

### 1. Controller
```bash
cd controller
go run main.go
```
Expected output:
```
PULL socket listening on :7777
PUB socket listening on :7778
Controller HTTP API listening on :8081
```

### 2. Scheduler
```bash
cd scheduler
go run main.go
```
Expected output:
```
Scheduler started, controller=http://localhost:8081, max-concurrent=24
Scheduler running, polling every 2s
```

### 3. API
```bash
cd api
go run main.go
```
Expected output:
```
[GIN-debug] Listening and serving HTTP on :8080
```

### 4. Worker
```bash
cd worker
go run main.go --worker-name worker1 --tags cpu
```
Expected output:
```
Connected to controller at localhost:7777
Registered as 'worker1' with RPC at localhost:XXXXX
Received API info: endpoint=http://localhost:8080
```

You can launch as many workers as you want in additional terminals. Each must have a unique `--worker-name`.

---

## API Usage

All endpoints except `/login` require a Bearer token in the `Authorization` header.

### Authentication

**Login**
```bash
curl -X POST -u <user>:<password> http://localhost:8080/login
```
Available users: `Carlos` / `carlos123`, `Demian` / `demian123`, `Fong` / `fong123`, `Loco` / `loco123`

Response:
```json
{"user": "Carlos", "token": "$TOKEN"}
```

**Logout**
```bash
curl -X DELETE -H "Authorization: Bearer <token>" http://localhost:8080/logout
```

### System Status

```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/status
```
Response:
```json
{
  "system_name": "DPIP Team 3",
  "server_time": "2026-05-30T12:53:33-06:00",
  "active_workloads": ["$WORKLOAD_NAME"]
}
```

### Workloads

**Create a workload**
```bash
curl -H "Authorization: Bearer <token>" -H "Content-Type: application/json" -X POST -d "{\"filter\": \"grayscale\", \"workload_name\": \"myjob\"}" http://localhost:8080/workloads
```
Available filters: `grayscale`, `blur`

Response:
```json
{
  "workload_id": "$WORKLOAD_ID",
  "filter": "grayscale",
  "workload_name": "myjob",
  "status": "scheduling",
  "running_jobs": 0,
  "filtered_images": []
}
```

**Get workload status**
```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/workloads/<workload_id>
```
The `status` field progresses: `scheduling` → `running` → `completed`.

### Images

**Upload an image for processing**
```bash
curl -H "Authorization: Bearer <token>" -F "data=@/path/to/image.png" -F "workload_id=<workload_id>" -F "type=original" -X POST http://localhost:8080/images
```
Response:
```json
{"image_id": "$IMAGE_ID", "workload_id": "$WORKLOAD_ID", "type": "original", "seq": 0}
```

Once uploaded, the worker automatically processes the image. Check the workload status to find the filtered image ID in `filtered_images`.

**Download a filtered image**
```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/images/<image_id> --output result.png
```

---

## Batch Video Processing

The `tests/` directory contains Python scripts to process a full video through the distributed system: extract its frames, push them to the workers, pull the filtered results, and reassemble them into a new video.

**Install dependencies:**
```bash
pip install -r tests/requirements.txt
```

**Extract frames from a video:**
```bash
# Linux/Mac
python3 tests/video_utils.py -action extract video.mp4 frames

# Windows
py tests/video_utils.py -action extract video.mp4 frames
```

**Push frames to the system:**
```bash
# Linux/Mac
python3 tests/stress_test.py -action push -workload-id $WORKLOAD_ID -token $TOKEN -frames-path frames

# Windows
py tests/stress_test.py -action push -workload-id $WORKLOAD_ID -token $TOKEN -frames-path frames
```

**Pull filtered frames:**
```bash
# Linux/Mac
python3 tests/stress_test.py -action pull -workload-id $WORKLOAD_ID -token $TOKEN -frames-path filtered-frames

# Windows
py tests/stress_test.py -action pull -workload-id $WORKLOAD_ID -token $TOKEN -frames-path filtered-frames
```

**Reassemble frames into a video:**
```bash
# Linux/Mac
python3 tests/video_utils.py -action join filtered.mp4 filtered-frames

# Windows
py tests/video_utils.py -action join filtered.mp4 filtered-frames
```

---

## Windows Notes

- The system is fully compatible with Windows. Temporary files use `os.TempDir()` (`%TEMP%`) instead of `/tmp/`.
- Controller nanomsg ports default to `7777`/`7778` to avoid Windows port reservation conflicts.
- On Windows, use `py` instead of `python3` for the test scripts.
- If `opencv-python` fails to install, try `pip install opencv-python-headless` instead.

---

## Known Limitations

- **In-memory state** — workloads, tokens, and image metadata are stored in memory. Restarting the API loses all state, even though the image files in `api/storage/` remain on disk.
- **No persistence layer** — there is no database; all data lives for the lifetime of the process.
- **Single host** — the current configuration assumes all components run on the same machine (`localhost`). Running across multiple machines requires passing explicit host flags to each component.

---

## Extra Points

Running the full Batch Video Processing flow above (extract → push → pull → join) covers **Extra 1 (+20%)**: video processing with the `tests/` tools.

**Extra 2 (+20%)** would require GPU/CUDA filtering in the worker nodes, which is not implemented in this version.

---

## Features for Future Work

1. **Persistent storage** — replace in-memory maps with a database (SQLite, PostgreSQL) so state survives restarts and scales across instances.
2. **Additional filter types** — extend the worker's filter pipeline to support sharpening, edge detection, color grading, or custom kernel convolutions.
3. **GPU-accelerated filtering** — compile a CUDA kernel for the worker so image processing offloads to the GPU, enabling significantly higher throughput for large frame batches.