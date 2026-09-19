package main

// logger.go —— 轻量日志：控制台 + 文件（logs/app.log，按大小切割，保留 5 份）

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu    sync.Mutex
	level int
	file  *os.File
	path  string
	size  int64
	max   int64
}

var log *Logger

var levelNames = map[int]string{0: "DEBUG", 1: "INFO", 2: "WARN", 3: "ERROR"}

func parseLevel(s string) int {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return 0
	case "INFO", "":
		return 1
	case "WARN", "WARNING":
		return 2
	case "ERROR":
		return 3
	}
	return 1
}

func setupLogger(level string) {
	dir := "logs"
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "app.log")
	log = &Logger{level: parseLevel(level), path: path, max: 10 * 1024 * 1024}
	log.openFile()
}

func (l *Logger) openFile() {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
	l.file = f
}

func (l *Logger) write(lv int, format string, args ...interface{}) {
	if lv < l.level {
		return
	}
	msg := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"),
		levelNames[lv], fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(os.Stdout, msg)
	if l.file == nil {
		return
	}
	if l.size+int64(len(msg)) > l.max {
		l.file.Close()
		_ = os.Rename(l.path, fmt.Sprintf("%s.%s", l.path, time.Now().Format("20060102150405")))
		// 清理历史，保留最近 5 份
		matches, _ := filepath.Glob(l.path + ".*")
		if len(matches) > 5 {
			for _, m := range matches[:len(matches)-5] {
				_ = os.Remove(m)
			}
		}
		l.size = 0
		l.openFile()
		if l.file == nil {
			return
		}
	}
	n, _ := l.file.WriteString(msg)
	l.size += int64(n)
}

func (l *Logger) Debugf(f string, a ...interface{}) { l.write(0, f, a...) }
func (l *Logger) Infof(f string, a ...interface{})  { l.write(1, f, a...) }
func (l *Logger) Warnf(f string, a ...interface{})  { l.write(2, f, a...) }
func (l *Logger) Errorf(f string, a ...interface{}) { l.write(3, f, a...) }
