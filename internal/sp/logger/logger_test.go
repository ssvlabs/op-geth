package logger

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {

	tests := []struct {
		name          string
		level         string
		pretty        bool
		expectedLevel zerolog.Level
	}{
		{
			name:          "trace level",
			level:         "trace",
			pretty:        false,
			expectedLevel: zerolog.TraceLevel,
		},
		{
			name:          "debug level",
			level:         "debug",
			pretty:        false,
			expectedLevel: zerolog.DebugLevel,
		},
		{
			name:          "info level",
			level:         "info",
			pretty:        false,
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "warn level",
			level:         "warn",
			pretty:        false,
			expectedLevel: zerolog.WarnLevel,
		},
		{
			name:          "error level",
			level:         "error",
			pretty:        false,
			expectedLevel: zerolog.ErrorLevel,
		},
		{
			name:          "fatal level",
			level:         "fatal",
			pretty:        false,
			expectedLevel: zerolog.FatalLevel,
		},
		{
			name:          "panic level",
			level:         "panic",
			pretty:        false,
			expectedLevel: zerolog.PanicLevel,
		},
		{
			name:          "uppercase level",
			level:         "INFO",
			pretty:        false,
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "mixed case level",
			level:         "WaRn",
			pretty:        false,
			expectedLevel: zerolog.WarnLevel,
		},
		{
			name:          "invalid level defaults to info",
			level:         "invalid",
			pretty:        false,
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "empty level defaults to info",
			level:         "",
			pretty:        false,
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "pretty output enabled",
			level:         "info",
			pretty:        true,
			expectedLevel: zerolog.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			originalLevel := zerolog.GlobalLevel()
			defer zerolog.SetGlobalLevel(originalLevel)

			logger := New(tt.level, tt.pretty)

			require.NotNil(t, logger)
			require.NotNil(t, logger.Logger)

			assert.Equal(t, tt.expectedLevel, zerolog.GlobalLevel())

			assert.NotPanics(t, func() {
				logger.Info().Msg("test message")
			})
		})
	}
}

func TestLogger_With(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	originalOutput := os.Stdout
	defer func() { os.Stdout = originalOutput }()

	zlog := zerolog.New(&buf).With().Timestamp().Logger()
	logger := Logger{zlog}

	childLogger := logger.With().Str("key", "value").Logger()
	require.NotNil(t, childLogger)

	childLogger.Info().Msg("test message")

	output := buf.String()
	assert.Contains(t, output, `"key":"value"`)
	assert.Contains(t, output, `"message":"test message"`)
	assert.Contains(t, output, `"level":"info"`)
}

func TestLogger_Module(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		moduleName string
	}{
		{
			name:       "simple module name",
			moduleName: "auth",
		},
		{
			name:       "module with special characters",
			moduleName: "user-service",
		},
		{
			name:       "module with numbers",
			moduleName: "api-v2",
		},
		{
			name:       "empty module name",
			moduleName: "",
		},
		{
			name:       "module with spaces",
			moduleName: "user service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			zlog := zerolog.New(&buf).With().Timestamp().Logger()
			logger := Logger{zlog}

			moduleLogger := logger.Module(tt.moduleName)
			require.NotNil(t, moduleLogger)

			moduleLogger.Info().Msg("test message")

			output := buf.String()
			assert.Contains(t, output, `"module":"`+tt.moduleName+`"`)
			assert.Contains(t, output, `"message":"test message"`)
			assert.Contains(t, output, `"level":"info"`)
		})
	}
}

func TestLogger_Integration(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zlog := zerolog.New(&buf).With().Timestamp().Logger()
	logger := Logger{zlog}

	moduleLogger := logger.Module("auth").With().Str("user_id", "123").Logger()
	require.NotNil(t, moduleLogger)

	moduleLogger.Info().Str("action", "login").Msg("user login attempt")

	output := buf.String()
	assert.Contains(t, output, `"module":"auth"`)
	assert.Contains(t, output, `"user_id":"123"`)
	assert.Contains(t, output, `"action":"login"`)
	assert.Contains(t, output, `"message":"user login attempt"`)
	assert.Contains(t, output, `"level":"info"`)
}

func TestLogger_DifferentLogLevels(t *testing.T) {

	tests := []struct {
		name        string
		level       string
		logFunc     func(Logger)
		shouldLog   bool
		expectedMsg string
	}{
		{
			name:        "trace level logs trace",
			level:       "trace",
			logFunc:     func(l Logger) { l.Trace().Msg("trace message") },
			shouldLog:   true,
			expectedMsg: "trace message",
		},
		{
			name:        "info level skips trace",
			level:       "info",
			logFunc:     func(l Logger) { l.Trace().Msg("trace message") },
			shouldLog:   false,
			expectedMsg: "",
		},
		{
			name:        "debug level logs debug",
			level:       "debug",
			logFunc:     func(l Logger) { l.Debug().Msg("debug message") },
			shouldLog:   true,
			expectedMsg: "debug message",
		},
		{
			name:        "info level skips debug",
			level:       "info",
			logFunc:     func(l Logger) { l.Debug().Msg("debug message") },
			shouldLog:   false,
			expectedMsg: "",
		},
		{
			name:        "info level logs info",
			level:       "info",
			logFunc:     func(l Logger) { l.Info().Msg("info message") },
			shouldLog:   true,
			expectedMsg: "info message",
		},
		{
			name:        "warn level skips info",
			level:       "warn",
			logFunc:     func(l Logger) { l.Info().Msg("info message") },
			shouldLog:   false,
			expectedMsg: "",
		},
		{
			name:        "warn level logs warn",
			level:       "warn",
			logFunc:     func(l Logger) { l.Warn().Msg("warn message") },
			shouldLog:   true,
			expectedMsg: "warn message",
		},
		{
			name:        "error level logs error",
			level:       "error",
			logFunc:     func(l Logger) { l.Error().Msg("error message") },
			shouldLog:   true,
			expectedMsg: "error message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			originalLevel := zerolog.GlobalLevel()
			defer zerolog.SetGlobalLevel(originalLevel)

			var buf bytes.Buffer
			zlog := zerolog.New(&buf).With().Timestamp().Logger()
			logger := Logger{zlog}

			New(tt.level, false)

			tt.logFunc(logger)

			output := buf.String()
			if tt.shouldLog {
				assert.Contains(t, output, tt.expectedMsg)
			} else {
				assert.Empty(t, output)
			}
		})
	}
}

func TestLogger_JSONOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zlog := zerolog.New(&buf).With().Timestamp().Logger()
	logger := Logger{zlog}

	logger.Info().
		Str("user_id", "123").
		Int("request_count", 5).
		Bool("is_admin", true).
		Dur("duration", 150*time.Millisecond).
		Msg("user request processed")

	output := buf.String()
	require.NotEmpty(t, output)

	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(strings.TrimSpace(output)), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "info", logEntry["level"])
	assert.Equal(t, "user request processed", logEntry["message"])
	assert.Equal(t, "123", logEntry["user_id"])
	assert.Equal(t, float64(5), logEntry["request_count"])
	assert.Equal(t, true, logEntry["is_admin"])
	assert.Contains(t, logEntry, "time")
	assert.Contains(t, logEntry, "duration")
}

func TestLogger_ErrorLogging(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zlog := zerolog.New(&buf).With().Timestamp().Logger()
	logger := Logger{zlog}

	err := &testError{msg: "test error"}

	logger.Error().Err(err).Msg("error occurred")

	output := buf.String()
	assert.Contains(t, output, `"level":"error"`)
	assert.Contains(t, output, `"message":"error occurred"`)
	assert.Contains(t, output, `"error":"test error"`)
	assert.NotEmpty(t, output)
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func BenchmarkLogger_Info(b *testing.B) {
	var buf bytes.Buffer
	zlog := zerolog.New(&buf).With().Timestamp().Logger()
	logger := Logger{zlog}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Info().Str("key", "value").Msg("benchmark message")
		}
	})
}
