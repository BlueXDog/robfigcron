package main

import (
	"context"
	"fmt"
	"github.com/robfig/cron/v3"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// JobStatus represents the status of our cron job
type JobStatus struct {
	LastRun time.Time
	Count   int
	mu      sync.RWMutex
}

// JobMetrics represents detailed metrics for a cron job
type JobMetrics struct {
	ID       cron.EntryID
	Name     string
	Running  bool
	LastRun  time.Time
	Duration time.Duration
	Errors   []error
	mu       sync.RWMutex
}

// JobManager handles cron jobs with advanced features
type JobManager struct {
	cron    *cron.Cron
	metrics map[cron.EntryID]*JobMetrics
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
}

// NewJobManager creates a new JobManager instance
func NewJobManager() *JobManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobManager{
		cron:    cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger))),
		metrics: make(map[cron.EntryID]*JobMetrics),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (jm *JobManager) AddJobWithTimeout(spec string, name string, timeout time.Duration, job func(context.Context)) (cron.EntryID, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	// Create metrics first with a zero ID
	metric := &JobMetrics{
		Name: name,
	}

	id, err := jm.cron.AddFunc(spec, func() {
		ctx, cancel := context.WithTimeout(jm.ctx, timeout)
		defer cancel()

		metric.mu.Lock()
		metric.Running = true
		metric.LastRun = time.Now()
		metric.mu.Unlock()

		done := make(chan struct{})
		go func() {
			job(ctx)
			close(done)
		}()

		select {
		case <-ctx.Done():
			jm.logError(metric.ID, fmt.Errorf("job %s timed out after %v", name, timeout))
		case <-done:
			metric.mu.Lock()
			metric.Duration = time.Since(metric.LastRun)
			metric.Running = false
			metric.mu.Unlock()
		}
	})

	if err != nil {
		return id, err
	}

	// Set the ID after we get it from cron.AddFunc
	metric.ID = id
	jm.metrics[id] = metric
	return id, nil
}

func (jm *JobManager) logError(id cron.EntryID, err error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	
	if metric, exists := jm.metrics[id]; exists {
		metric.mu.Lock()
		metric.Errors = append(metric.Errors, err)
		metric.Running = false
		metric.mu.Unlock()
	}
}

func (jm *JobManager) GetMetrics(id cron.EntryID) (JobMetrics, bool) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	
	metric, exists := jm.metrics[id]
	if !exists {
		return JobMetrics{}, false
	}
	
	metric.mu.RLock()
	defer metric.mu.RUnlock()
	return *metric, true
}

func main() {
	// Initialize job manager
	jm := NewJobManager()

	// Add a job that runs every minute with a 30-second timeout
	jobID, err := jm.AddJobWithTimeout("* * * * *", "status-update", 30*time.Second, func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		default:
			fmt.Printf("Cron job executed at: %v\n", time.Now())
		}
	})

	if err != nil {
		log.Fatalf("Failed to add job: %v", err)
	}

	// Start the cron scheduler
	jm.cron.Start()

	// Create HTTP server
	mux := http.NewServeMux()
	
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Welcome to the Cron Web Server!")
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		metrics, exists := jm.GetMetrics(jobID)
		if !exists {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}
		
		fmt.Fprintf(w, "Job: %s\nLast Run: %v\nRunning: %v\nDuration: %v\nErrors: %d\n",
			metrics.Name,
			metrics.LastRun.Format(time.RFC3339),
			metrics.Running,
			metrics.Duration,
			len(metrics.Errors),
		)
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// Channel to listen for errors coming from the listener.
	serverErrors := make(chan error, 1)

	// Start the server
	go func() {
		log.Printf("Server starting on %s", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	// Channel to listen for interrupt/terminate signals
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Blocking main and waiting for shutdown.
	select {
	case err := <-serverErrors:
		log.Printf("Error starting server: %v", err)
		
	case sig := <-shutdown:
		log.Printf("Start shutdown... Signal: %v", sig)
		
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		// Trigger cancellation for all jobs
		jm.cancel()

		// Stop cron scheduler
		cronCtx := jm.cron.Stop()

		// Shutdown http server
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server Shutdown: %v", err)
		}

		// Wait for cron jobs to finish
		select {
		case <-cronCtx.Done():
			log.Println("Cron jobs finished successfully")
		case <-shutdownCtx.Done():
			log.Println("Timeout waiting for cron jobs to finish")
		}
	}
}

func (status *JobStatus) handleHTTP(w http.ResponseWriter, r *http.Request) {
	status.mu.RLock()
	defer status.mu.RUnlock()
	
	fmt.Fprintf(w, "Last Run: %v\nTotal Executions: %d\n", 
		status.LastRun.Format(time.RFC3339),
		status.Count,
	)
}