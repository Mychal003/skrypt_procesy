package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Monitor struct {
	command     string
	logFile     string
	timeout     time.Duration
	interval    time.Duration
	process     *exec.Cmd
	lastModTime time.Time
}

func NewMonitor(command, logFile string, timeout, interval int) *Monitor {
	return &Monitor{
		command:  command,
		logFile:  logFile,
		timeout:  time.Duration(timeout) * time.Second,
		interval: time.Duration(interval) * time.Second,
	}
}

func (m *Monitor) getLogModTime() (time.Time, error) {
	info, err := os.Stat(m.logFile)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func (m *Monitor) startProcess() error {
	if m.process != nil {
		m.killProcess()
	}

	fmt.Printf("Uruchamianie: %s\n", m.command)
	m.process = exec.Command("sh", "-c", m.command)
	
	err := m.process.Start()
	if err != nil {
		return fmt.Errorf("nie można uruchomić procesu: %v", err)
	}

	fmt.Printf("Proces uruchomiony z PID: %d\n", m.process.Process.Pid)
	return nil
}

func (m *Monitor) killProcess() {
	if m.process == nil || m.process.Process == nil {
		return
	}

	fmt.Printf("Zatrzymywanie procesu PID: %d\n", m.process.Process.Pid)
	
	// Najpierw spróbuj delikatnie
	m.process.Process.Signal(syscall.SIGTERM)
	
	// Czekaj 5 sekund
	done := make(chan error, 1)
	go func() {
		done <- m.process.Wait()
	}()

	select {
	case <-done:
		fmt.Println("Proces zakończony poprawnie")
	case <-time.After(5 * time.Second):
		fmt.Println("Wymuszanie zakończenia procesu...")
		m.process.Process.Kill()
		m.process.Wait()
	}
	
	m.process = nil
}

func (m *Monitor) isProcessRunning() bool {
	if m.process == nil || m.process.Process == nil {
		return false
	}

	// Sprawdź czy proces jeszcze żyje
	err := m.process.Process.Signal(syscall.Signal(0))
	return err == nil
}

func (m *Monitor) checkLogs() (bool, error) {
	modTime, err := m.getLogModTime()
	if err != nil {
		return false, fmt.Errorf("nie można odczytać pliku logów: %v", err)
	}

	// Jeśli to pierwsze sprawdzenie
	if m.lastModTime.IsZero() {
		m.lastModTime = modTime
		return true, nil
	}

	// Sprawdź czy logi się zmieniły
	if modTime.After(m.lastModTime) {
		fmt.Printf("Logi zaktualizowane: %s\n", modTime.Format("15:04:05"))
		m.lastModTime = modTime
		return true, nil
	}

	// Sprawdź timeout
	if time.Since(m.lastModTime) > m.timeout {
		fmt.Printf("TIMEOUT! Brak zmian w logach przez %v\n", m.timeout)
		return false, nil
	}

	return true, nil
}

func (m *Monitor) Run() {
	// Obsługa sygnałów
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Uruchom proces
	if err := m.startProcess(); err != nil {
		log.Fatalf("Błąd uruchamiania: %v", err)
	}

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	fmt.Printf("Monitor uruchomiony (timeout: %v, interwał: %v)\n", m.timeout, m.interval)

	for {
		select {
		case <-sigChan:
			fmt.Println("\nOtrzymano sygnał zamknięcia...")
			m.killProcess()
			return

		case <-ticker.C:
			// Sprawdź czy proces jeszcze żyje
			if !m.isProcessRunning() {
				fmt.Println("Proces nie żyje, restartowanie...")
				if err := m.startProcess(); err != nil {
					log.Printf("Błąd restartu: %v", err)
					continue
				}
				// Reset czasu ostatniej zmiany
				m.lastModTime = time.Now()
				continue
			}

			// Sprawdź logi
			ok, err := m.checkLogs()
			if err != nil {
				log.Printf("Błąd sprawdzania logów: %v", err)
				continue
			}

			if !ok {
				fmt.Println("Restartowanie z powodu braku aktywności w logach...")
				m.killProcess()
				if err := m.startProcess(); err != nil {
					log.Printf("Błąd restartu: %v", err)
					continue
				}
				m.lastModTime = time.Now()
			}
		}
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("Użycie: %s <komenda> <plik_logów> [timeout_sek] [interwał_sek]\n", os.Args[0])
		fmt.Println("Przykład:")
		fmt.Printf("  %s \"python3 app.py\" \"/tmp/app.log\" 60 5\n", os.Args[0])
		os.Exit(1)
	}

	command := os.Args[1]
	logFile := os.Args[2]

	timeout := 60  // domyślnie 60 sekund
	interval := 5  // domyślnie 5 sekund

	if len(os.Args) > 3 {
		if t, err := strconv.Atoi(os.Args[3]); err == nil && t > 0 {
			timeout = t
		}
	}

	if len(os.Args) > 4 {
		if i, err := strconv.Atoi(os.Args[4]); err == nil && i > 0 {
			interval = i
		}
	}

	monitor := NewMonitor(command, logFile, timeout, interval)
	monitor.Run()
}