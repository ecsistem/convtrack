// Package videojobs implementa uma fila assíncrona em memória para a
// camuflagem de vídeo: os vídeos entram na fila, são processados por workers
// em background e o resultado fica disponível para download.
//
// O estado é mantido em memória e os arquivos (entrada/saída) em disco, num
// diretório temporário. Reiniciar o processo limpa a fila e os resultados.
package videojobs

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/google/uuid"
)

// Status do job na fila.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusDone       Status = "done"
	StatusError      Status = "error"
)

// Job é a visão pública (serializável) de um job da fila.
type Job struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"` // exposto só na listagem admin (cross-projeto)
	Filename  string    `json:"filename"`
	Preset    string    `json:"preset"`
	Topic     string    `json:"topic"`
	Status    Status    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Progress  int       `json:"progress"` // 0–100 (durante o processamento)
	Size      int64     `json:"size"`
	Frames    int       `json:"frames"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type internalJob struct {
	Job
	projectID  string
	inputPath  string
	resultPath string
	req        shield.CamoVideoRequest
}

// Queue é a fila de jobs com workers em background.
type Queue struct {
	mu    sync.Mutex
	jobs  map[string]*internalJob
	order []string // ordem de criação (ids)
	dir   string
	ch    chan string

	maxPerProject int
}

// New cria a fila, prepara o diretório de trabalho e inicia `workers` workers.
func New(dir string, workers int) (*Queue, error) {
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "videocamo_jobs")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if workers < 1 {
		workers = 1
	}
	q := &Queue{
		jobs:          map[string]*internalJob{},
		dir:           dir,
		ch:            make(chan string, 256),
		maxPerProject: 50,
	}
	for i := 0; i < workers; i++ {
		go q.worker()
	}
	return q, nil
}

// Enqueue grava o vídeo em disco, cria o job e o coloca na fila.
func (q *Queue) Enqueue(projectID, filename, ext string, input []byte, req shield.CamoVideoRequest, preset, topic string) (Job, error) {
	id := uuid.NewString()
	jobDir := filepath.Join(q.dir, id)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return Job{}, err
	}
	inputPath := filepath.Join(jobDir, "input"+ext)
	if err := os.WriteFile(inputPath, input, 0644); err != nil {
		return Job{}, err
	}

	now := time.Now()
	ij := &internalJob{
		Job: Job{
			ID:        id,
			Filename:  filename,
			Preset:    preset,
			Topic:     topic,
			Status:    StatusQueued,
			CreatedAt: now,
			UpdatedAt: now,
		},
		projectID:  projectID,
		inputPath:  inputPath,
		resultPath: filepath.Join(jobDir, "output.mp4"),
		req:        req,
	}

	q.mu.Lock()
	q.jobs[id] = ij
	q.order = append(q.order, id)
	q.evictOldLocked(projectID)
	q.mu.Unlock()

	q.ch <- id
	return ij.Job, nil
}

// List devolve os jobs de um projeto, do mais recente para o mais antigo.
func (q *Queue) List(projectID string) []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Job
	for i := len(q.order) - 1; i >= 0; i-- {
		ij := q.jobs[q.order[i]]
		if ij != nil && ij.projectID == projectID {
			out = append(out, ij.Job)
		}
	}
	return out
}

// ListAll devolve todos os jobs de todos os projetos (uso exclusivo do admin),
// do mais recente para o mais antigo, com ProjectID preenchido.
func (q *Queue) ListAll() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Job
	for i := len(q.order) - 1; i >= 0; i-- {
		ij := q.jobs[q.order[i]]
		if ij == nil {
			continue
		}
		job := ij.Job
		job.ProjectID = ij.projectID
		out = append(out, job)
	}
	return out
}

// Get devolve um job específico (validando o projeto).
func (q *Queue) Get(projectID, id string) (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ij := q.jobs[id]
	if ij == nil || ij.projectID != projectID {
		return Job{}, false
	}
	return ij.Job, true
}

// ResultPath devolve o caminho do resultado se o job estiver concluído.
func (q *Queue) ResultPath(projectID, id string) (string, string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ij := q.jobs[id]
	if ij == nil || ij.projectID != projectID || ij.Status != StatusDone {
		return "", "", false
	}
	return ij.resultPath, ij.Filename, true
}

// ResultPathAny é a versão sem checagem de projeto, para o admin baixar o
// resultado de qualquer conta.
func (q *Queue) ResultPathAny(id string) (string, string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ij := q.jobs[id]
	if ij == nil || ij.Status != StatusDone {
		return "", "", false
	}
	return ij.resultPath, ij.Filename, true
}

// Delete remove um job e seus arquivos.
func (q *Queue) Delete(projectID, id string) bool {
	q.mu.Lock()
	ij := q.jobs[id]
	if ij == nil || ij.projectID != projectID {
		q.mu.Unlock()
		return false
	}
	delete(q.jobs, id)
	for i, oid := range q.order {
		if oid == id {
			q.order = append(q.order[:i], q.order[i+1:]...)
			break
		}
	}
	q.mu.Unlock()
	os.RemoveAll(filepath.Dir(ij.inputPath))
	return true
}

// evictOldLocked remove jobs concluídos/antigos além do limite por projeto.
func (q *Queue) evictOldLocked(projectID string) {
	var ids []string
	for _, id := range q.order {
		if ij := q.jobs[id]; ij != nil && ij.projectID == projectID {
			ids = append(ids, id)
		}
	}
	for len(ids) > q.maxPerProject {
		oldest := ids[0]
		ids = ids[1:]
		if ij := q.jobs[oldest]; ij != nil {
			os.RemoveAll(filepath.Dir(ij.inputPath))
		}
		delete(q.jobs, oldest)
		for i, oid := range q.order {
			if oid == oldest {
				q.order = append(q.order[:i], q.order[i+1:]...)
				break
			}
		}
	}
}

func (q *Queue) worker() {
	for id := range q.ch {
		q.process(id)
	}
}

func (q *Queue) process(id string) {
	q.mu.Lock()
	ij := q.jobs[id]
	if ij == nil {
		q.mu.Unlock()
		return
	}
	ij.Status = StatusProcessing
	ij.UpdatedAt = time.Now()
	inputPath := ij.inputPath
	req := ij.req
	q.mu.Unlock()

	data, err := os.ReadFile(inputPath)
	if err != nil {
		q.fail(id, fmt.Errorf("ler vídeo: %w", err))
		return
	}
	req.VideoData = data

	// callback de progresso → atualiza o job (com throttle por %)
	lastPct := -1
	req.Progress = func(done, total int) {
		if total <= 0 {
			return
		}
		pct := done * 100 / total
		if pct == lastPct {
			return
		}
		lastPct = pct
		q.mu.Lock()
		if cur := q.jobs[id]; cur != nil {
			cur.Progress = pct
			cur.UpdatedAt = time.Now()
		}
		q.mu.Unlock()
	}

	start := time.Now()
	log.Printf("[videocamo] job %s: processando (%d bytes)…", id, len(data))

	res, err := shield.CamouflageVideo(req)
	if err != nil {
		log.Printf("[videocamo] job %s: ERRO após %s: %v", id, time.Since(start).Round(time.Second), err)
		q.fail(id, err)
		return
	}
	if err := os.WriteFile(ij.resultPath, res.VideoData, 0644); err != nil {
		q.fail(id, fmt.Errorf("salvar resultado: %w", err))
		return
	}
	log.Printf("[videocamo] job %s: pronto em %s (%d frames, %d bytes)",
		id, time.Since(start).Round(time.Second), res.Frames, len(res.VideoData))

	q.mu.Lock()
	if cur := q.jobs[id]; cur != nil {
		cur.Status = StatusDone
		cur.Progress = 100
		cur.Size = int64(len(res.VideoData))
		cur.Frames = res.Frames
		cur.UpdatedAt = time.Now()
	}
	q.mu.Unlock()

	// libera o input (não é mais necessário)
	os.Remove(inputPath)
}

func (q *Queue) fail(id string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ij := q.jobs[id]; ij != nil {
		ij.Status = StatusError
		ij.Error = err.Error()
		ij.UpdatedAt = time.Now()
	}
}
