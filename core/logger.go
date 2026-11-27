package core

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ANSI escape codes для цветов (256 цветовая палитра)
func ansiColor(code int) string {
	return fmt.Sprintf("\x1b[38;5;%dm", code)
}

func ansiReset() string {
	return "\x1b[0m"
}

// gradientText раскрашивает текст градиентом от startColor к endColor.
func gradientText(text string, startColor, endColor int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return ""
	}

	var sb strings.Builder
	for i, ch := range runes {
		c := startColor
		if n > 1 {
			c = startColor + (endColor-startColor)*i/(n-1)
		}
		sb.WriteString(ansiColor(c))
		sb.WriteRune(ch)
	}
	sb.WriteString(ansiReset())
	return sb.String()
}

// Logger is a small levelled console logger. All proxies share one instance
// (see Log) so that the debug switch applies everywhere.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	debug atomic.Bool
	color bool
}

// Log is the process-wide logger.
var Log = &Logger{out: os.Stdout, color: true}

// NewLogger returns the shared logger. It is kept for compatibility with older code.
func NewLogger() *Logger { return Log }

// SetDebug enables or disables Debug output.
func (l *Logger) SetDebug(on bool) { l.debug.Store(on) }

// DebugEnabled reports whether Debug output is enabled.
func (l *Logger) DebugEnabled() bool { return l.debug.Load() }

// SetOutput redirects the logger (used by tests).
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out = w
}

func (l *Logger) prefix(level string) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	if !l.color {
		return fmt.Sprintf("%s %-7s|", now, level)
	}
	var coloredLevel string
	switch level {
	case "INFO":
		coloredLevel = gradientText(" INFO  ", 39, 51)
	case "WARN":
		coloredLevel = gradientText(" WARN  ", 220, 214)
	case "ERROR":
		coloredLevel = gradientText(" ERROR ", 196, 160)
	case "DEBUG":
		coloredLevel = gradientText(" DEBUG ", 245, 250)
	default:
		coloredLevel = level
	}
	return fmt.Sprintf("%s %s |", now, coloredLevel)
}

func (l *Logger) write(level, format string, a ...interface{}) {
	line := l.prefix(level) + " " + fmt.Sprintf(format, a...) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.out, line)
}

// Info logs an informational message.
func (l *Logger) Info(format string, a ...interface{}) { l.write("INFO", format, a...) }

// Warn logs a warning.
func (l *Logger) Warn(format string, a ...interface{}) { l.write("WARN", format, a...) }

// Error logs an error.
func (l *Logger) Error(format string, a ...interface{}) { l.write("ERROR", format, a...) }

// Debug logs a message only when debug mode is on.
func (l *Logger) Debug(format string, a ...interface{}) {
	if l.debug.Load() {
		l.write("DEBUG", format, a...)
	}
}
