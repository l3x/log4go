// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

package log4go

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const testLogFile = "_logtest.log"

var now time.Time = time.Unix(0, 1234567890123456789).In(time.UTC)

func newLogRecord(lvl Level, src string, msg string) *LogRecord {
	return &LogRecord{
		Level:   lvl,
		Source:  src,
		Created: now,
		Message: msg,
	}
}

func TestELog(t *testing.T) {
	fmt.Printf("Testing %s\n", L4G_VERSION)
	lr := newLogRecord(CRITICAL, "source", "message")
	if lr.Level != CRITICAL {
		t.Errorf("Incorrect level: %d should be %d", lr.Level, CRITICAL)
	}
	if lr.Source != "source" {
		t.Errorf("Incorrect source: %s should be %s", lr.Source, "source")
	}
	if lr.Message != "message" {
		t.Errorf("Incorrect message: %s should be %s", lr.Source, "message")
	}
}

var formatTests = []struct {
	Test    string
	Record  *LogRecord
	Formats map[string]string
}{
	{
		Test: "Standard formats",
		Record: &LogRecord{
			Level:   ERROR,
			Source:  "source",
			Message: "message",
			Created: now,
		},
		Formats: map[string]string{
			// TODO(kevlar): How can I do this so it'll work outside of PST?
			FORMAT_DEFAULT: "[2009/02/13 23:31:30 UTC] [EROR] (source) message\n",
			FORMAT_MILLIS:  "[2009/02/13 23:31:30.123] [EROR] (source) message\n",
			FORMAT_SHORT:   "[23:31 02/13/09] [EROR] message\n",
			FORMAT_ABBREV:  "[EROR] message\n",
		},
	},
}

func TestFormatLogRecord(t *testing.T) {
	for _, test := range formatTests {
		name := test.Test
		for fmt, want := range test.Formats {
			if got := FormatLogRecord(fmt, test.Record); got != want {
				t.Errorf("%s - %s:", name, fmt)
				t.Errorf("   got %q", got)
				t.Errorf("  want %q", want)
			}
		}
	}
}

var logRecordWriteTests = []struct {
	Test    string
	Record  *LogRecord
	Console string
}{
	{
		Test: "Normal message",
		Record: &LogRecord{
			Level:   CRITICAL,
			Source:  "source",
			Message: "message",
			Created: now,
		},
		Console: "[02/13/09 23:31:30] [CRIT] message\n",
	},
}

func TestConsoleLogWriter(t *testing.T) {
	console := ConsoleLogWriterImp{
		records:   make(chan *LogRecord, LogBufferLength),
		completed: make(chan int),
	}

	r, w := io.Pipe()
	go console.run(w)
	defer console.Close()

	buf := make([]byte, 1024)

	for _, test := range logRecordWriteTests {
		name := test.Test

		console.LogWrite(test.Record)
		n, _ := r.Read(buf)

		if got, want := string(buf[:n]), test.Console; got != want {
			t.Errorf("%s:  got %q", name, got)
			t.Errorf("%s: want %q", name, want)
		}
	}
}

func TestFileLogWriter(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	w := NewFileLogWriter(testLogFile, false, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	defer os.Remove(testLogFile)

	w.LogWrite(newLogRecord(CRITICAL, "source", "message"))
	w.Close()
	runtime.Gosched()

	if contents, err := ioutil.ReadFile(testLogFile); err != nil {
		t.Errorf("read(%q): %s", testLogFile, err)
	} else if len(contents) != 50 {
		t.Errorf("malformed filelog: %q (%d bytes)", string(contents), len(contents))
	}
}

func TestFileLogRotation(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 5

	w := NewFileLogWriter(testLogFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(time.Now().Add(-1 * 24 * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	os.Remove(testLogFile)
	files, _ := filepath.Glob(testLogFile + ".*")
	for _, file := range files {
		os.Remove(file)
	}

	// Ensure we deleted the expected number of rotations
	if len(files) != rotations {
		t.Fatalf("Incorrect number of file rotations occurred: Expected %d, got %d", rotations, len(files))
	}
}

func TestFileLogRotationUnderFailureConditions(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	if runtime.GOOS == "windows" {
		t.Skip()
	}

	testLogDir, dirErr := ioutil.TempDir("/tmp", "_log4go")
	if dirErr != nil {
		t.Fatalf("Couldn't create temp directory: %v", dirErr)
	}
	defer os.RemoveAll(testLogDir)
	currentFile := filepath.Join(testLogDir, testLogFile)

	w := NewFileLogWriter(currentFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}

	w.LogWrite(newLogRecord(CRITICAL, "source", "foo"))
	w.LogWrite(newLogRecord(CRITICAL, "source", "bar"))
	runtime.Gosched()

	// Remove log directory
	os.RemoveAll(testLogDir)

	// Force rotation
	w.Rotate()
	runtime.Gosched()

	// Logging should continue at "current" file
	w.LogWrite(newLogRecord(CRITICAL, "source", "baz"))
	if _, statErr := os.Stat(currentFile); statErr != nil {
		t.Fatalf("Couldn't stat logfile: %v", statErr)
	}

	// Save old permissions
	var oldMode os.FileMode
	if fileInfo, statErr := os.Stat(currentFile); statErr != nil {
		t.Fatalf("Couldn't stat current logfile: %v", statErr)
	} else {
		oldMode = fileInfo.Mode()
	}

	// Make it impossible to read/write file
	if err := os.Chmod(currentFile, 0000); err != nil {
		t.Fatalf("Couldn't reduce permissions: %v", err)
	}

	// Force file handle closure
	w.file.Close()

	// Log message, will be discarded, library will not crash
	w.LogWrite(newLogRecord(CRITICAL, "source", "this will be dropped"))
	runtime.Gosched()

	// Restore previous permissions
	if err := os.Chmod(currentFile, oldMode); err != nil {
		t.Fatalf("Couldn't restore permissions: %v", err)
	}

	// Reopen log file to restore operation
	if err := w.openLogFile(); err != nil {
		t.Fatalf("Unable to reopen log: %v", err)
	}

	// Log message, will not be dropped
	w.LogWrite(newLogRecord(CRITICAL, "source", "this will be saved"))
	runtime.Gosched()
	w.Close()

	// Make it impossible to read/write file
	if err := os.Chmod(currentFile, 0000); err != nil {
		t.Fatalf("Couldn't reduce permissions: %v", err)
	}

	// Try to open new logger
	w2 := NewFileLogWriter(currentFile, true, false)
	if w2 != nil {
		t.Fatalf("New logger creation should fail on permissions")
	}
}

func TestFileLogFailureReporting(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	testLogDir, dirErr := ioutil.TempDir("", "_log4go")
	if dirErr != nil {
		t.Fatalf("Couldn't create temp directory: %v", dirErr)
	}
	defer os.RemoveAll(testLogDir)
	currentFile := filepath.Join(testLogDir, testLogFile)

	w := NewFileLogWriter(currentFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}

	// Write a message to open the file so we can close it
	w.LogWrite(newLogRecord(CRITICAL, "source", "foo"))
	for i := 0; w.maxlines_curlines == 0 || i == 1000; i++ { // flakey test avoidance
		runtime.Gosched()
	}

	// Make it impossible to report errors
	w.errorWriter = w.file

	// Force file handle closure
	w.file.Close()

	// Drop a message
	w.LogWrite(newLogRecord(CRITICAL, "source", "foo"))
	for i := 0; w.maxlines_curlines == 1 || i == 1000; i++ {
		runtime.Gosched()
	}

	// Restore operation
	b := make([]byte, 0)
	errBuffer := bytes.NewBuffer(b)
	w.errorWriter = errBuffer
	if err := w.openLogFile(); err != nil {
		t.Fatalf("Unable to reopen log: %v", err)
	}

	w.LogWrite(newLogRecord(CRITICAL, "source", "bar"))
	runtime.Gosched()
	for i := 0; w.maxlines_curlines == 0 || i == 1000; i++ {
		runtime.Gosched()
	}

	if !bytes.Contains(errBuffer.Bytes(), []byte("Dropped 1 previous log message(s)")) {
		t.Fatalf("Incorrect error message: %v", errBuffer.String())
	}

	if bytes.Count(errBuffer.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("Incorrect number of error messages")
	}

	w.Close()
}

func TestFileWriterArchive(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 100
	filesToKeep := 10
	now := time.Now()

	w := NewFileLogWriter(testLogFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)
	w.SetRotateDateSuffix(true)
	w.SetMaxArchiveFiles(filesToKeep)

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(now.Add(time.Duration((rotations-i)*-1*24) * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	err := os.Remove(testLogFile)
	if err != nil {
		t.Fatalf("Expected %s to exist", testLogFile)
	}
	files, _ := filepath.Glob(testLogFile + ".*")
	i := 0
	for _, file := range files {
		err = os.Remove(file)
		if err != nil {
			t.Fatalf("Expected %s to exist", file)
		}
		i++
	}

	if i != filesToKeep {
		t.Fatalf("Expected %d files to be left after archival, found %d", filesToKeep, i)
	}

	// Ensure the correct files were kept
	sort.Strings(files)
	for j := 0; j < filesToKeep; j++ {
		date := now.Add(time.Duration((filesToKeep-j)*-1*24) * time.Hour)
		dateStr := fmt.Sprintf("%04d-%02d-%02d", date.Year(), date.Month(), date.Day())
		if !strings.HasSuffix(files[j], dateStr) {
			t.Fatalf("Expected filename %q to have suffix %q", files[j], dateStr)
		}
	}
}

func TestFileWriterArchiveFewerThanFilesToKeep(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 5
	filesToKeep := 10

	w := NewFileLogWriter(testLogFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)
	w.SetRotateDateSuffix(true)
	w.SetMaxArchiveFiles(filesToKeep)

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(time.Now().Add(time.Duration((rotations-i)*-1*24) * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	err := os.Remove(testLogFile)
	if err != nil {
		t.Fatalf("Expected %s to exist", testLogFile)
	}
	files, _ := filepath.Glob(testLogFile + ".*")
	i := 0
	for _, file := range files {
		err = os.Remove(file)
		if err != nil {
			t.Fatalf("Expected %s to exist", file)
		}
		i++
	}

	if i != rotations {
		t.Fatalf("Expected %d files to be left after archival, found %d", rotations, i)
	}
}

func TestFileWriterArchivingDisabled(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 100

	w := NewFileLogWriter(testLogFile, true, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)
	w.SetRotateDateSuffix(true)
	w.SetMaxArchiveFiles(0)

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(time.Now().Add(time.Duration((rotations-i)*-1*24) * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	err := os.Remove(testLogFile)
	if err != nil {
		t.Fatalf("Expected %s to exist", testLogFile)
	}
	files, _ := filepath.Glob(testLogFile + ".*")
	i := 0
	for _, file := range files {
		err = os.Remove(file)
		if err != nil {
			t.Fatalf("Expected %s to exist", file)
		}
		i++
	}

	if i != rotations {
		t.Fatalf("Expected %d files to be left after archival, found %d", rotations, i)
	}
}

func TestFileWriterCompressionGz(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 100
	filesToKeep := 10

	w := NewFileLogWriter(testLogFile, true, true)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)
	w.SetRotateDateSuffix(true)
	w.SetMaxArchiveFiles(filesToKeep)
	w.SetCompressionMethod("gz")

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(time.Now().Add(time.Duration((rotations-i)*-1*24) * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	err := os.Remove(testLogFile)
	if err != nil {
		t.Fatalf("Expected %s to exist", testLogFile)
	}
	files, _ := filepath.Glob(testLogFile + ".*")
	i := 0
	for _, file := range files {
		if !strings.HasSuffix(file, ".gz") {
			t.Fatalf("Expected %s to have compressed file extension", file)
		}
		err = os.Remove(file)
		if err != nil {
			t.Fatalf("Expected %s to exist", file)
		}
		i++
	}

	if i != filesToKeep {
		t.Fatalf("Expected %d files to be left after archival, found %d", filesToKeep, i)
	}
}

func TestFileWriterCompressionZip(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	rotations := 100
	filesToKeep := 10

	w := NewFileLogWriter(testLogFile, true, true)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	w.SetRotateOnStartup(false)
	w.SetRotateDateSuffix(true)
	w.SetMaxArchiveFiles(filesToKeep)
	w.SetCompressionMethod("zip")

	for i := 0; i < rotations; i++ {
		w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", i)))
		runtime.Gosched()

		rotateErr := w.handleRotate(time.Now().Add(time.Duration((rotations-i)*-1*24) * time.Hour))
		if rotateErr != nil {
			t.Fatalf("Error occurred on rotation: %s", rotateErr.Error())
		}
	}

	// Write to current logfile once to make the ordering clear
	w.LogWrite(newLogRecord(CRITICAL, "source", fmt.Sprintf("msg %d", rotations)))
	w.Close()

	// Delete rotated files
	err := os.Remove(testLogFile)
	if err != nil {
		t.Fatalf("Expected %s to exist", testLogFile)
	}
	files, _ := filepath.Glob(testLogFile + ".*")
	i := 0
	for _, file := range files {
		if !strings.HasSuffix(file, ".zip") {
			t.Fatalf("Expected %s to have compressed file extension", file)
		}
		err = os.Remove(file)
		if err != nil {
			t.Fatalf("Expected %s to exist", file)
		}
		i++
	}

	if i != filesToKeep {
		t.Fatalf("Expected %d files to be left after archival, found %d", filesToKeep, i)
	}
}

func TestXMLLogWriter(t *testing.T) {
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	w := NewXMLLogWriter(testLogFile, false)
	if w == nil {
		t.Fatalf("Invalid return: w should not be nil")
	}
	defer os.Remove(testLogFile)

	w.LogWrite(newLogRecord(CRITICAL, "source", "message"))
	w.Close()
	for i := 0; w.maxlines_curlines == 0 || i == 1000; i++ {
		runtime.Gosched()
	}

	if contents, err := ioutil.ReadFile(testLogFile); err != nil {
		t.Errorf("read(%q): %s", testLogFile, err)
	} else if len(contents) != 185 {
		t.Errorf("malformed xmllog: %q (%d bytes)", string(contents), len(contents))
	}
}

func TestLogger(t *testing.T) {
	sl := NewDefaultLogger(WARNING)
	if sl == nil {
		t.Fatalf("NewDefaultLogger should never return nil")
	}
	if lw, exist := sl["stdout"]; lw == nil || exist != true {
		t.Fatalf("NewDefaultLogger produced invalid logger (DNE or nil)")
	}
	if sl["stdout"].Level != WARNING {
		t.Fatalf("NewDefaultLogger produced invalid logger (incorrect level)")
	}
	if len(sl) != 1 {
		t.Fatalf("NewDefaultLogger produced invalid logger (incorrect map count)")
	}

	//func (l *Logger) AddFilter(name string, level int, writer LogWriter) {}
	l := make(Logger)
	l.AddFilter("stdout", DEBUG, NewConsoleLogWriter())
	if lw, exist := l["stdout"]; lw == nil || exist != true {
		t.Fatalf("AddFilter produced invalid logger (DNE or nil)")
	}
	if l["stdout"].Level != DEBUG {
		t.Fatalf("AddFilter produced invalid logger (incorrect level)")
	}
	if len(l) != 1 {
		t.Fatalf("AddFilter produced invalid logger (incorrect map count)")
	}

	//func (l *Logger) Warn(format string, args ...interface{}) error {}
	if err := l.Warn("%s %d %#v", "Warning:", 1, []int{}); err.Error() != "Warning: 1 []int{}" {
		t.Errorf("Warn returned invalid error: %s", err)
	}

	//func (l *Logger) Error(format string, args ...interface{}) error {}
	if err := l.Error("%s %d %#v", "Error:", 10, []string{}); err.Error() != "Error: 10 []string{}" {
		t.Errorf("Error returned invalid error: %s", err)
	}

	//func (l *Logger) Critical(format string, args ...interface{}) error {}
	if err := l.Critical("%s %d %#v", "Critical:", 100, []int64{}); err.Error() != "Critical: 100 []int64{}" {
		t.Errorf("Critical returned invalid error: %s", err)
	}

	// Already tested or basically untestable
	//func (l *Logger) Log(level int, source, message string) {}
	//func (l *Logger) Logf(level int, format string, args ...interface{}) {}
	//func (l *Logger) intLogf(level int, format string, args ...interface{}) string {}
	//func (l *Logger) Finest(format string, args ...interface{}) {}
	//func (l *Logger) Fine(format string, args ...interface{}) {}
	//func (l *Logger) Debug(format string, args ...interface{}) {}
	//func (l *Logger) Trace(format string, args ...interface{}) {}
	//func (l *Logger) Info(format string, args ...interface{}) {}
}

func TestLogOutput(t *testing.T) {
	const (
		expected = "da59cbdd5a2cabbf550676cfb2913138"
	)

	// Unbuffered output
	defer func(buflen int) {
		LogBufferLength = buflen
	}(LogBufferLength)
	LogBufferLength = 0

	l := make(Logger)

	// Delete and open the output log without a timestamp (for a constant md5sum)
	l.AddFilter("file", FINEST, NewFileLogWriter(testLogFile, false, false).SetFormat("[%L] %M"))
	defer os.Remove(testLogFile)

	// Send some log messages
	l.Log(CRITICAL, "testsrc1", fmt.Sprintf("This message is level %d", int(CRITICAL)))
	l.Logf(ERROR, "This message is level %v", ERROR)
	l.Logf(WARNING, "This message is level %s", WARNING)
	l.Logc(INFO, func() string { return "This message is level INFO" })
	l.Trace("This message is level %d", int(TRACE))
	l.Debug("This message is level %s", DEBUG)
	l.Fine(func() string { return fmt.Sprintf("This message is level %v", FINE) })
	l.Finest("This message is level %v", FINEST)
	l.Finest(FINEST, "is also this message's level")

	l.Close()

	contents, err := ioutil.ReadFile(testLogFile)
	if err != nil {
		t.Fatalf("Could not read output log: %s", err)
	}

	sum := md5.New()
	sum.Write(contents)
	if sumstr := hex.EncodeToString(sum.Sum(nil)); sumstr != expected {
		t.Errorf("--- Log Contents:\n%s---", string(contents))
		t.Fatalf("Checksum does not match: %s (expecting %s)", sumstr, expected)
	}
}

func TestCountMallocs(t *testing.T) {
	const N = 1
	var m runtime.MemStats
	getMallocs := func() uint64 {
		runtime.ReadMemStats(&m)
		return m.Mallocs
	}

	// Console logger
	sl := NewDefaultLogger(INFO)
	mallocs := 0 - getMallocs()
	for i := 0; i < N; i++ {
		sl.Log(WARNING, "here", "This is a WARNING message")
	}
	mallocs += getMallocs()
	fmt.Printf("mallocs per sl.Log((WARNING, \"here\", \"This is a log message\"): %d\n", mallocs/N)

	// Console logger formatted
	mallocs = 0 - getMallocs()
	for i := 0; i < N; i++ {
		sl.Logf(WARNING, "%s is a log message with level %d", "This", WARNING)
	}
	mallocs += getMallocs()
	fmt.Printf("mallocs per sl.Logf(WARNING, \"%%s is a log message with level %%d\", \"This\", WARNING): %d\n", mallocs/N)

	// Console logger (not logged)
	sl = NewDefaultLogger(INFO)
	mallocs = 0 - getMallocs()
	for i := 0; i < N; i++ {
		sl.Log(DEBUG, "here", "This is a DEBUG log message")
	}
	mallocs += getMallocs()
	fmt.Printf("mallocs per unlogged sl.Log((WARNING, \"here\", \"This is a log message\"): %d\n", mallocs/N)

	// Console logger formatted (not logged)
	mallocs = 0 - getMallocs()
	for i := 0; i < N; i++ {
		sl.Logf(DEBUG, "%s is a log message with level %d", "This", DEBUG)
	}
	mallocs += getMallocs()
	fmt.Printf("mallocs per unlogged sl.Logf(WARNING, \"%%s is a log message with level %%d\", \"This\", WARNING): %d\n", mallocs/N)
}

func TestXMLConfig(t *testing.T) {
	const (
		configfile = "example.xml"
	)

	fd, err := os.Create(configfile)
	if err != nil {
		t.Fatalf("Could not open %s for writing: %s", configfile, err)
	}

	fmt.Fprintln(fd, "<logging>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>stdout</tag>")
	fmt.Fprintln(fd, "    <type>console</type>")
	fmt.Fprintln(fd, "    <!-- level is (:?FINEST|FINE|DEBUG|TRACE|INFO|WARNING|ERROR) -->")
	fmt.Fprintln(fd, "    <level>DEBUG</level>")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>file</tag>")
	fmt.Fprintln(fd, "    <type>file</type>")
	fmt.Fprintln(fd, "    <level>FINEST</level>")
	fmt.Fprintln(fd, "    <property name=\"filename\">test.log</property>")
	fmt.Fprintln(fd, "    <!--")
	fmt.Fprintln(fd, "       %T - Time (15:04:05 MST)")
	fmt.Fprintln(fd, "       %t - Time (15:04)")
	fmt.Fprintln(fd, "       %D - Date (2006/01/02)")
	fmt.Fprintln(fd, "       %d - Date (01/02/06)")
	fmt.Fprintln(fd, "       %L - Level (FNST, FINE, DEBG, TRAC, WARN, EROR, CRIT)")
	fmt.Fprintln(fd, "       %S - Source")
	fmt.Fprintln(fd, "       %M - Message")
	fmt.Fprintln(fd, "       It ignores unknown format strings (and removes them)")
	fmt.Fprintln(fd, "       Recommended: \"[%D %T] [%L] (%S) %M\"")
	fmt.Fprintln(fd, "    -->")
	fmt.Fprintln(fd, "    <property name=\"format\">[%D %T] [%L] (%S) %M</property>")
	fmt.Fprintln(fd, "    <property name=\"rotate\">false</property> <!-- true enables log rotation, otherwise append -->")
	fmt.Fprintln(fd, "    <property name=\"maxsize\">0M</property> <!-- \\d+[KMG]? Suffixes are in terms of 2**10 -->")
	fmt.Fprintln(fd, "    <property name=\"maxlines\">0K</property> <!-- \\d+[KMG]? Suffixes are in terms of thousands -->")
	fmt.Fprintln(fd, "    <property name=\"daily\">true</property> <!-- Automatically rotates when a log message is written after midnight -->")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>xmllog</tag>")
	fmt.Fprintln(fd, "    <type>xml</type>")
	fmt.Fprintln(fd, "    <level>TRACE</level>")
	fmt.Fprintln(fd, "    <property name=\"filename\">trace.xml</property>")
	fmt.Fprintln(fd, "    <property name=\"rotate\">true</property> <!-- true enables log rotation, otherwise append -->")
	fmt.Fprintln(fd, "    <property name=\"maxsize\">100M</property> <!-- \\d+[KMG]? Suffixes are in terms of 2**10 -->")
	fmt.Fprintln(fd, "    <property name=\"maxrecords\">6K</property> <!-- \\d+[KMG]? Suffixes are in terms of thousands -->")
	fmt.Fprintln(fd, "    <property name=\"daily\">false</property> <!-- Automatically rotates when a log message is written after midnight -->")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "  <filter enabled=\"false\"><!-- enabled=false means this logger won't actually be created -->")
	fmt.Fprintln(fd, "    <tag>donotopen</tag>")
	fmt.Fprintln(fd, "    <type>socket</type>")
	fmt.Fprintln(fd, "    <level>FINEST</level>")
	fmt.Fprintln(fd, "    <property name=\"endpoint\">192.168.1.255:12124</property> <!-- recommend UDP broadcast -->")
	fmt.Fprintln(fd, "    <property name=\"protocol\">udp</property> <!-- tcp or udp -->")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "</logging>")
	fd.Close()

	log := make(Logger)
	log.LoadConfiguration(configfile)
	defer os.Remove("trace.xml")
	defer os.Remove("test.log")
	defer log.Close()

	// Make sure we got all loggers
	if len(log) != 3 {
		t.Fatalf("XMLConfig: Expected 3 filters, found %d", len(log))
	}

	// Make sure they're the right keys
	if _, ok := log["stdout"]; !ok {
		t.Errorf("XMLConfig: Expected stdout logger")
	}
	if _, ok := log["file"]; !ok {
		t.Fatalf("XMLConfig: Expected file logger")
	}
	if _, ok := log["xmllog"]; !ok {
		t.Fatalf("XMLConfig: Expected xmllog logger")
	}

	// Make sure they're the right type
	if _, ok := log["stdout"].LogWriter.(ConsoleLogWriter); !ok {
		t.Fatalf("XMLConfig: Expected stdout to be ConsoleLogWriter, found %T", log["stdout"].LogWriter)
	}
	if _, ok := log["file"].LogWriter.(*FileLogWriter); !ok {
		t.Fatalf("XMLConfig: Expected file to be *FileLogWriter, found %T", log["file"].LogWriter)
	}
	if _, ok := log["xmllog"].LogWriter.(*FileLogWriter); !ok {
		t.Fatalf("XMLConfig: Expected xmllog to be *FileLogWriter, found %T", log["xmllog"].LogWriter)
	}

	// Make sure levels are set
	if lvl := log["stdout"].Level; lvl != DEBUG {
		t.Errorf("XMLConfig: Expected stdout to be set to level %d, found %d", DEBUG, lvl)
	}
	if lvl := log["file"].Level; lvl != FINEST {
		t.Errorf("XMLConfig: Expected file to be set to level %d, found %d", FINEST, lvl)
	}
	if lvl := log["xmllog"].Level; lvl != TRACE {
		t.Errorf("XMLConfig: Expected xmllog to be set to level %d, found %d", TRACE, lvl)
	}

	// Make sure the w is open and points to the right file
	if fname := log["file"].LogWriter.(*FileLogWriter).file.Name(); fname != "test.log" {
		t.Errorf("XMLConfig: Expected file to have opened %s, found %s", "test.log", fname)
	}

	// Make sure the XLW is open and points to the right file
	if fname := log["xmllog"].LogWriter.(*FileLogWriter).file.Name(); fname != "trace.xml" {
		t.Errorf("XMLConfig: Expected xmllog to have opened %s, found %s", "trace.xml", fname)
	}

	// Move XML log file
	os.Rename(configfile, "examples/"+configfile) // Keep this so that an example with the documentation is available
}

func TestExpandEnvironmentVariables(t *testing.T) {
	const (
		configfile = "example.xml"
		logVar     = "variable"
	)

	os.Setenv("log.location", logVar)

	fd, err := os.Create(configfile)
	if err != nil {
		t.Fatalf("Could not open %s for writing: %s", configfile, err)
	}

	fmt.Fprintln(fd, "<logging>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>file</tag>")
	fmt.Fprintln(fd, "    <type>file</type>")
	fmt.Fprintln(fd, "    <level>FINEST</level>")
	fmt.Fprintln(fd, "    <property name=\"filename\">${log.location}-test</property>")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "</logging>")
	fd.Close()

	log := make(Logger)
	log.LoadConfiguration(configfile)
	defer os.Remove(logVar + "-test")
	defer log.Close()

	// Make sure we got all loggers
	if len(log) != 1 {
		t.Fatalf("XMLConfig: Expected 1 filters, found %d", len(log))
	}
	if _, ok := log["file"]; !ok {
		t.Fatalf("XMLConfig: Expected file logger")
	}

	// Make sure the w points to the right file
	if fname := log["file"].LogWriter.(*FileLogWriter).file.Name(); fname != logVar+"-test" {
		t.Errorf("XMLConfig: Expected file to have opened %s, found %s", logVar+"-test", fname)
	}
}

func TestEscapeExpandEnvironmentVariables(t *testing.T) {
	const (
		configfile = "example.xml"
		logVar     = "variable"
	)

	os.Setenv("log.location", logVar)

	fd, err := os.Create(configfile)
	if err != nil {
		t.Fatalf("Could not open %s for writing: %s", configfile, err)
	}

	fmt.Fprintln(fd, "<logging>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>file</tag>")
	fmt.Fprintln(fd, "    <type>file</type>")
	fmt.Fprintln(fd, "    <level>FINEST</level>")
	fmt.Fprintln(fd, "    <property name=\"filename\">\\$log.location-test</property>")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "</logging>")
	fd.Close()

	log := make(Logger)
	log.LoadConfiguration(configfile)
	defer os.Remove("$log.location-test")
	defer log.Close()

	// Make sure we got all loggers
	if len(log) != 1 {
		t.Fatalf("XMLConfig: Expected 1 filters, found %d", len(log))
	}
	if _, ok := log["file"]; !ok {
		t.Fatalf("XMLConfig: Expected file logger")
	}

	// Make sure the w points to the right file
	if fname := log["file"].LogWriter.(*FileLogWriter).file.Name(); fname != "$log.location-test" {
		t.Errorf("XMLConfig: Expected file to have opened %s, found %s", logVar+"-test", fname)
	}
}

func TestXMLMissingDir(t *testing.T) {
	const (
		configfile = "example.xml"
	)

	fd, err := os.Create(configfile)
	if err != nil {
		t.Fatalf("Could not open %s for writing: %s", configfile, err)
	}

	fmt.Fprintln(fd, "<logging>")
	fmt.Fprintln(fd, "  <filter enabled=\"true\">")
	fmt.Fprintln(fd, "    <tag>file</tag>")
	fmt.Fprintln(fd, "    <type>file</type>")
	fmt.Fprintln(fd, "    <level>FINEST</level>")
	fmt.Fprintln(fd, "    <property name=\"filename\">test/test.log</property>")
	fmt.Fprintln(fd, "    <property name=\"format\">[%D %T] [%L] (%S) %M</property>")
	fmt.Fprintln(fd, "    <property name=\"rotate\">false</property> <!-- true enables log rotation, otherwise append -->")
	fmt.Fprintln(fd, "    <property name=\"maxsize\">0M</property> <!-- \\d+[KMG]? Suffixes are in terms of 2**10 -->")
	fmt.Fprintln(fd, "    <property name=\"maxlines\">0K</property> <!-- \\d+[KMG]? Suffixes are in terms of thousands -->")
	fmt.Fprintln(fd, "    <property name=\"daily\">true</property> <!-- Automatically rotates when a log message is written after midnight -->")
	fmt.Fprintln(fd, "  </filter>")
	fmt.Fprintln(fd, "</logging>")
	fd.Close()

	log := make(Logger)
	log.LoadConfiguration(configfile)
	defer os.Remove("trace.xml")
	defer os.RemoveAll("test")
	defer log.Close()

}

func TestMultipleExpansions(t *testing.T) {
	valString := "${a}xhj${bc}${def}dkwk"
	os.Setenv("a", "1")
	os.Setenv("bc", "2")
	os.Setenv("def", "345")
	replaced := substituteEnv(valString)
	if replaced != "1xhj2345dkwk" {
		t.Errorf("Variable expansion: Expected %q, but got %q", "1xhj2345dkwk", replaced)
	}
}

func TestEscapedExpansions(t *testing.T) {
	valString := "${a}xhj\\${bc}${def}dkwk"
	os.Setenv("a", "1")
	os.Setenv("bc", "2")
	os.Setenv("def", "345")
	replaced := substituteEnv(valString)
	if replaced != "1xhj${bc}345dkwk" {
		t.Errorf("Variable expansion: Expected %q, but got %q", "1xhj${bc}345dkwk", replaced)
	}
}

func TestEscapedBackspace(t *testing.T) {
	valString := "${a}xhj\\\\${bc}${def}dkwk"
	os.Setenv("a", "1")
	os.Setenv("bc", "2")
	os.Setenv("def", "345")
	replaced := substituteEnv(valString)
	if replaced != "1xhj\\2345dkwk" {
		t.Errorf("Variable expansion: Expected %q, but got %q", "1xhj\\2345dkwk", replaced)
	}
}

func BenchmarkFormatLogRecord(b *testing.B) {
	const updateEvery = 1
	rec := &LogRecord{
		Level:   CRITICAL,
		Created: now,
		Source:  "source",
		Message: "message",
	}
	for i := 0; i < b.N; i++ {
		rec.Created = rec.Created.Add(1 * time.Second / updateEvery)
		if i%2 == 0 {
			FormatLogRecord(FORMAT_DEFAULT, rec)
		} else {
			FormatLogRecord(FORMAT_SHORT, rec)
		}
	}
}

func BenchmarkConsoleLog(b *testing.B) {
	/* This doesn't seem to work on OS X
	sink, err := os.Open(os.DevNull)
	if err != nil {
		panic(err)
	}
	if err := syscall.Dup2(int(sink.Fd()), syscall.Stdout); err != nil {
		panic(err)
	}
	*/

	stdout = ioutil.Discard
	sl := NewDefaultLogger(INFO)
	for i := 0; i < b.N; i++ {
		sl.Log(WARNING, "here", "This is a log message")
	}
}

func BenchmarkConsoleNotLogged(b *testing.B) {
	sl := NewDefaultLogger(INFO)
	for i := 0; i < b.N; i++ {
		sl.Log(DEBUG, "here", "This is a log message")
	}
}

func BenchmarkConsoleUtilLog(b *testing.B) {
	sl := NewDefaultLogger(INFO)
	for i := 0; i < b.N; i++ {
		sl.Info("%s is a log message", "This")
	}
}

func BenchmarkConsoleUtilNotLog(b *testing.B) {
	sl := NewDefaultLogger(INFO)
	for i := 0; i < b.N; i++ {
		sl.Debug("%s is a log message", "This")
	}
}

func BenchmarkFileLog(b *testing.B) {
	sl := make(Logger)
	b.StopTimer()
	sl.AddFilter("file", INFO, NewFileLogWriter("benchlog.log", false, false))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		sl.Log(WARNING, "here", "This is a log message")
	}
	b.StopTimer()
	os.Remove("benchlog.log")
}

func BenchmarkFileNotLogged(b *testing.B) {
	sl := make(Logger)
	b.StopTimer()
	sl.AddFilter("file", INFO, NewFileLogWriter("benchlog.log", false, false))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		sl.Log(DEBUG, "here", "This is a log message")
	}
	b.StopTimer()
	os.Remove("benchlog.log")
}

func BenchmarkFileUtilLog(b *testing.B) {
	sl := make(Logger)
	b.StopTimer()
	sl.AddFilter("file", INFO, NewFileLogWriter("benchlog.log", false, false))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		sl.Info("%s is a log message", "This")
	}
	b.StopTimer()
	os.Remove("benchlog.log")
}

func BenchmarkFileUtilNotLog(b *testing.B) {
	sl := make(Logger)
	b.StopTimer()
	sl.AddFilter("file", INFO, NewFileLogWriter("benchlog.log", false, false))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		sl.Debug("%s is a log message", "This")
	}
	b.StopTimer()
	os.Remove("benchlog.log")
}

// Benchmark results (darwin amd64 6g)
//elog.BenchmarkConsoleLog           100000       22819 ns/op
//elog.BenchmarkConsoleNotLogged    2000000         879 ns/op
//elog.BenchmarkConsoleUtilLog        50000       34380 ns/op
//elog.BenchmarkConsoleUtilNotLog   1000000        1339 ns/op
//elog.BenchmarkFileLog              100000       26497 ns/op
//elog.BenchmarkFileNotLogged       2000000         821 ns/op
//elog.BenchmarkFileUtilLog           50000       33945 ns/op
//elog.BenchmarkFileUtilNotLog      1000000        1258 ns/op
