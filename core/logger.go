package core

import (
	"fmt"
	"strings"
	"time"
)

// ANSI escape codes для цветов (256 цветовая палитра)
func ansiColor(code int) string {
	return fmt.Sprintf("\x1b[38;5;%dm", code)
}

func ansiReset() string {
	return "\x1b[0m"
}

// Пример градиента от цвета startColor к endColor по длине текста
func gradientText(text string, startColor, endColor int) string {
	n := len(text)
	if n == 0 {
		return ""
	}

	var sb strings.Builder
	for i, ch := range text {
		// Линейная интерполяция цвета
		c := startColor + (endColor-startColor)*i/(n-1)
		sb.WriteString(ansiColor(c))
		sb.WriteRune(ch)
	}
	sb.WriteString(ansiReset())
	return sb.String()
}

type Logger struct{}

func NewLogger() *Logger {
	return &Logger{}
}

func (l *Logger) prefix(level string) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	var coloredLevel string
	switch level {
	case "INFO":
		// Градиент от голубого к циановому (цвета 39 -> 51)
		coloredLevel = gradientText(" INFO ", 39, 51)
	case "WARN":
		// Градиент от жёлтого к оранжевому (цвета 220 -> 214)
		coloredLevel = gradientText(" WARN ", 220, 214)
	case "ERROR":
		// Градиент от красного к ярко-красному (196 -> 160)
		coloredLevel = gradientText(" ERROR ", 196, 160)
	default:
		coloredLevel = level
	}
	return fmt.Sprintf("%s %s |", now, coloredLevel)
}

func (l *Logger) Info(format string, a ...interface{}) {
	fmt.Printf("%s "+format+"\n", append([]interface{}{l.prefix("INFO")}, a...)...)
}

func (l *Logger) Warn(format string, a ...interface{}) {
	fmt.Printf("%s "+format+"\n", append([]interface{}{l.prefix("WARN")}, a...)...)
}

func (l *Logger) Error(format string, a ...interface{}) {
	fmt.Printf("%s "+format+"\n", append([]interface{}{l.prefix("ERROR")}, a...)...)
}
