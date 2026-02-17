package utils

import (
	"fmt"
	"log"
	"os"
	"time"
)

// Logger provides structured, leveled logging throughout the application.
type Logger struct {
	info  *log.Logger
	warn  *log.Logger
	err   *log.Logger
	debug *log.Logger
}

// NewLogger creates a new Logger writing to stdout/stderr.
func NewLogger() *Logger {
	flags := 0
	return &Logger{
		info:  log.New(os.Stdout, "", flags),
		warn:  log.New(os.Stdout, "", flags),
		err:   log.New(os.Stderr, "", flags),
		debug: log.New(os.Stdout, "", flags),
	}
}

func (l *Logger) timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func (l *Logger) Info(format string, args ...any) {
	l.info.Printf(fmt.Sprintf("[%s] \033[32mINFO\033[0m  %s\n", l.timestamp(), format), args...)
}

func (l *Logger) Warn(format string, args ...any) {
	l.warn.Printf(fmt.Sprintf("[%s] \033[33mWARN\033[0m  %s\n", l.timestamp(), format), args...)
}

func (l *Logger) Error(format string, args ...any) {
	l.err.Printf(fmt.Sprintf("[%s] \033[31mERROR\033[0m %s\n", l.timestamp(), format), args...)
}

func (l *Logger) Debug(format string, args ...any) {
	l.debug.Printf(fmt.Sprintf("[%s] \033[36mDEBUG\033[0m %s\n", l.timestamp(), format), args...)
}
