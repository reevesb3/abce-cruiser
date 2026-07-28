package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gen2brain/beeep"
)

const logDir = "logs"

var logFile *os.File

func initLog() {
	_ = os.MkdirAll(logDir, 0o755)
	f, err := os.OpenFile(filepath.Join(logDir, "signup-log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		logFile = f
	}
}

func logf(format string, a ...any) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, a...))
	fmt.Println(line)
	if logFile != nil {
		fmt.Fprintln(logFile, line)
	}
}

func fatalf(format string, a ...any) {
	logf("ERROR: "+format, a...)
	os.Exit(1)
}

// Native desktop notification (Windows toast / macOS notification center) plus
// an audible fallback.
func toast(title, msg string) {
	if err := beeep.Notify(title, msg, ""); err != nil {
		logf("Toast failed: %v", err)
	}
	for i := 0; i < 6; i++ {
		_ = beeep.Beep(1200, 250)
		time.Sleep(120 * time.Millisecond)
	}
}
