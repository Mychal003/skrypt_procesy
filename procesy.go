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

// Struktura przechowująca konfigurację monitora
type Monitor struct {
	command     string        // Komenda do uruchomienia
	logFile     string        // Ścieżka do pliku logów
	timeout     time.Duration // Jak długo czekać bez zmian w logach
	interval    time.Duration // Jak często sprawdzać
	process     *exec.Cmd     // Wskaźnik do uruchomionego procesu
	lastModTime time.Time     // Kiedy ostatnio zmieniły się logi
	lastLogSize int64         // Ostatni rozmiar pliku logów
}

// Konstruktor - tworzy nową instancję monitora
func NewMonitor(command, logFile string, timeout, interval int) *Monitor {
	return &Monitor{
		command:  command,
		logFile:  logFile,
		timeout:  time.Duration(timeout) * time.Second,
		interval: time.Duration(interval) * time.Second,
	}
}

// Pobiera informacje o pliku logów (czas modyfikacji i rozmiar)
func (m *Monitor) getLogInfo() (time.Time, int64, error) {
	info, err := os.Stat(m.logFile)
	if err != nil {
		return time.Time{}, 0, err
	}
	return info.ModTime(), info.Size(), nil
}

// Sprawdza czy w logach pojawiły się nowe wpisy
func (m *Monitor) checkLogs() (bool, error) {
	modTime, size, err := m.getLogInfo()
	if err != nil {
		return false, fmt.Errorf("nie można odczytać pliku logów: %v", err)
	}

	// Pierwsza iteracja - zapisz początkowy stan
	if m.lastModTime.IsZero() {
		m.lastModTime = modTime
		m.lastLogSize = size
		fmt.Printf("Początkowy stan logów: rozmiar %d bajtów\n", size)
		return true, nil
	}

	// Sprawdź czy plik urósł (nowe logi)
	if size > m.lastLogSize {
		fmt.Printf("Nowe logi: rozmiar %d -> %d bajtów (+%d)\n", 
			m.lastLogSize, size, size-m.lastLogSize)
		m.lastModTime = time.Now()
		m.lastLogSize = size
		return true, nil
	}

	// Sprawdź czy plik się zmienił (może został przepisany)
	if modTime.After(m.lastModTime) {
		fmt.Printf("Plik logów zaktualizowany: %s\n", modTime.Format("15:04:05"))
		m.lastModTime = modTime
		m.lastLogSize = size
		return true, nil
	}

	// Sprawdź czy minął timeout bez zmian
	timeSinceLastChange := time.Since(m.lastModTime)
	if timeSinceLastChange > m.timeout {
		fmt.Printf("TIMEOUT! Brak zmian w logach przez %v (limit: %v)\n", 
			timeSinceLastChange.Round(time.Second), m.timeout)
		return false, nil
	}

	// Pokazuj co jakiś czas status oczekiwania
	if int(timeSinceLastChange.Seconds())%30 == 0 && timeSinceLastChange > 30*time.Second {
		fmt.Printf("Oczekiwanie na zmiany w logach... (%v/%v)\n", 
			timeSinceLastChange.Round(time.Second), m.timeout)
	}

	return true, nil
}

// Uruchamia nowy proces
func (m *Monitor) startProcess() error {
	// Jeśli jakiś proces już działa, zabij go
	if m.process != nil {
		m.killProcess()
	}

	fmt.Printf("Uruchamianie: %s\n", m.command)
	
	// Tworzenie komendy do wykonania
	m.process = exec.Command("sh", "-c", m.command)
	
	// Uruchomienie procesu w tle
	err := m.process.Start()
	if err != nil {
		return fmt.Errorf("nie można uruchomić procesu: %v", err)
	}

	fmt.Printf("Proces uruchomiony z PID: %d\n", m.process.Process.Pid)
	
	// Reset metryk - nowy proces = nowy start
	m.lastModTime = time.Now()
	
	return nil
}

// Zabija proces - najpierw grzecznie (SIGTERM), potem na siłę (SIGKILL)
func (m *Monitor) killProcess() {
	if m.process == nil || m.process.Process == nil {
		return
	}

	pid := m.process.Process.Pid
	fmt.Printf("Zatrzymywanie procesu PID: %d\n", pid)
	
	// Wyślij SIGTERM (grzeczne zamknięcie)
	err := m.process.Process.Signal(syscall.SIGTERM)
	if err != nil {
		fmt.Printf("Błąd wysyłania SIGTERM: %v\n", err)
		return
	}
	
	// Uruchom goroutine która czeka na zakończenie procesu
	done := make(chan error, 1)
	go func() {
		done <- m.process.Wait()
	}()

	// Czekaj maksymalnie 5 sekund na grzeczne zamknięcie
	select {
	case err := <-done:
		if err != nil {
			fmt.Printf("Proces zakończony z błędem: %v\n", err)
		} else {
			fmt.Println("Proces zakończony poprawnie")
		}
	case <-time.After(5 * time.Second):
		// Timeout - zabij na siłę
		fmt.Println("Wymuszanie zakończenia procesu (SIGKILL)...")
		m.process.Process.Kill()
		m.process.Wait()
		fmt.Println("Proces zakończony wymuszenie")
	}
	
	m.process = nil
}

// Sprawdza czy proces jeszcze żyje
func (m *Monitor) isProcessRunning() bool {
	if m.process == nil || m.process.Process == nil {
		return false
	}

	// Wyślij sygnał 0 - nie zabija procesu, tylko sprawdza czy istnieje
	err := m.process.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Główna pętla monitora
func (m *Monitor) Run() {
	fmt.Println("Uruchamianie monitora procesów...")
	fmt.Printf("Plik logów: %s\n", m.logFile)
	fmt.Printf("Timeout: %v\n", m.timeout)
	fmt.Printf("Interwał sprawdzania: %v\n", m.interval)
	fmt.Println("Aby zatrzymać monitor, naciśnij Ctrl+C")
	fmt.Println("--------------------------------------------------")

	// Obsługa sygnałów systemowych (Ctrl+C, kill)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Uruchom proces po raz pierwszy
	if err := m.startProcess(); err != nil {
		log.Fatalf("Błąd uruchamiania: %v", err)
	}

	// Timer sprawdzający stan co określony interwał
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Główna pętla
	for {
		select {
		case sig := <-sigChan:
			// Otrzymano sygnał zamknięcia
			fmt.Printf("\nOtrzymano sygnał %v, zamykanie monitora...\n", sig)
			m.killProcess()
			fmt.Println("Monitor zakończony")
			return

		case <-ticker.C:
			// Czas na kolejne sprawdzenie
			needRestart := false
			reason := ""

			// 1. Sprawdź czy proces jeszcze żyje
			if !m.isProcessRunning() {
				needRestart = true
				reason = "proces przestał działać"
			}

			// 2. Sprawdź aktywność w logach (tylko jeśli proces żyje)
			if !needRestart {
				logOk, err := m.checkLogs()
				if err != nil {
					log.Printf("Błąd sprawdzania logów: %v", err)
					continue
				}
				if !logOk {
					needRestart = true
					reason = "brak aktywności w logach"
				}
			}

			// 3. Jeśli trzeba, restartuj proces
			if needRestart {
				fmt.Printf("Restartowanie procesu - powód: %s\n", reason)
				m.killProcess()
				
				if err := m.startProcess(); err != nil {
					log.Printf("Błąd restartu: %v", err)
					// Spróbuj ponownie za interwał
					continue
				}
				
				fmt.Println("Proces zrestartowany pomyślnie")
			}
		}
	}
}

// Wyświetla instrukcję użycia
func printUsage(progName string) {
	fmt.Printf("🔍 Monitor Procesów - automatyczny restart przy braku aktywności\n\n")
	fmt.Printf("Użycie: %s <komenda> <plik_logów> [timeout_sek] [interwał_sek]\n\n", progName)
	fmt.Printf("Parametry:\n")
	fmt.Printf("  komenda      - aplikacja do monitorowania (w cudzysłowach)\n")
	fmt.Printf("  plik_logów   - ścieżka do pliku z logami\n")
	fmt.Printf("  timeout_sek  - restart po X sekundach bez zmian (domyślnie: 60)\n")
	fmt.Printf("  interwał_sek - sprawdzaj co X sekund (domyślnie: 5)\n\n")
	fmt.Printf("Przykłady:\n")
	fmt.Printf("  %s \"python3 app.py > /tmp/app.log 2>&1\" \"/tmp/app.log\"\n", progName)
	fmt.Printf("  %s \"java -jar app.jar\" \"/var/log/app.log\" 120 10\n", progName)
	fmt.Printf("  %s \"./moj_skrypt.sh\" \"/tmp/output.log\" 30 3\n", progName)
	fmt.Printf("\nNotatki:\n")
	fmt.Printf("  • Monitor restartuje proces gdy logi nie zmieniają się przez określony czas\n")
	fmt.Printf("  • Proces jest najpierw grzecznie zamykany (SIGTERM), potem na siłę (SIGKILL)\n")
	fmt.Printf("  • Aby zatrzymać monitor, użyj Ctrl+C\n")
}

func main() {
	// Sprawdzenie argumentów
	if len(os.Args) < 3 {
		printUsage(os.Args[0])
		os.Exit(1)
	}

	// Parsowanie argumentów
	command := os.Args[1]
	logFile := os.Args[2]

	// Domyślne wartości
	timeout := 60  // 60 sekund timeout
	interval := 5  // sprawdzaj co 5 sekund

	// Opcjonalne argumenty
	if len(os.Args) > 3 {
		if t, err := strconv.Atoi(os.Args[3]); err == nil && t > 0 {
			timeout = t
		} else {
			fmt.Printf("Nieprawidłowy timeout '%s', używam domyślnego: %d\n", os.Args[3], timeout)
		}
	}

	if len(os.Args) > 4 {
		if i, err := strconv.Atoi(os.Args[4]); err == nil && i > 0 {
			interval = i
		} else {
			fmt.Printf("Nieprawidłowy interwał '%s', używam domyślnego: %d\n", os.Args[4], interval)
		}
	}

	// Walidacja parametrów
	if timeout < interval {
		fmt.Printf("Timeout (%d) jest mniejszy niż interwał (%d), może to prowadzić do częstych restartów\n", timeout, interval)
	}

	// Sprawdź czy plik logów istnieje (jeśli nie, spróbuj go utworzyć)
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		fmt.Printf("Plik logów nie istnieje, tworzę: %s\n", logFile)
		if file, err := os.Create(logFile); err != nil {
			fmt.Printf("Nie można utworzyć pliku logów: %v\n", err)
			os.Exit(1)
		} else {
			file.Close()
		}
	}

	// Utworzenie i uruchomienie monitora
	monitor := NewMonitor(command, logFile, timeout, interval)
	monitor.Run()
}