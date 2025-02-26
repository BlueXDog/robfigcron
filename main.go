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

func main() {
	// Initialize job status
	status := &JobStatus{}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize cron with recovery middleware
	c := cron.New(
		cron.WithChain(
			cron.Recover(cron.DefaultLogger), // Recover from panics
		),
	)

	// Add a job that runs every minute
	c.AddFunc("* * * * *", func() {
		// Check if context is done
		select {
		case <-ctx.Done():
			return
		default:
			status.mu.Lock()
			status.LastRun = time.Now()
			status.Count++
			status.mu.Unlock()
			
			fmt.Printf("Cron job executed at: %v (Count: %d)\n", status.LastRun, status.Count)
		}
	})

	// Start the cron scheduler
	c.Start()

	// Create HTTP server
	mux := http.NewServeMux()
	
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Welcome to the Cron Web Server!")
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		status.mu.RLock()
		defer status.mu.RUnlock()
		
		fmt.Fprintf(w, "Last Run: %v\nTotal Executions: %d\n", 
			status.LastRun.Format(time.RFC3339),
			status.Count,
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
		
		// Create shutdown context with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		// Trigger cancellation for cron jobs
		cancel()

		// Stop cron scheduler (this waits for running jobs to complete)
		cronCtx := c.Stop()

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